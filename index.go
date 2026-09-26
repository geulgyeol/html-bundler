package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/fxamacker/cbor/v2"
	"github.com/geulgyeol/html-bundler/bundler"
	"github.com/geulgyeol/html-bundler/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

type bundleEntry struct {
	Offset int64
	URL    string
}

// indexBundle replaces an object's index atomically, so retrying a retained file is safe.
func indexBundle(ctx context.Context, pool *pgxpool.Pool, key string, entries []bundleEntry) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	queries := db.New(tx)
	id, err := queries.UpsertBundle(ctx, key)
	if err != nil {
		return err
	}
	if err := queries.DeleteBundleEntries(ctx, id); err != nil {
		return err
	}
	offsets := make([]int64, 0, len(entries))
	urls := make([]string, 0, len(entries))
	for _, entry := range entries {
		offsets = append(offsets, entry.Offset)
		urls = append(urls, entry.URL)
	}
	if len(entries) > 0 {
		if err := queries.InsertBundleEntries(ctx, db.InsertBundleEntriesParams{BundleID: id, FrameOffsets: offsets, Urls: urls}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// readBundleEntries scans a completed bundle without loading compressed HTML into memory.
func readBundleEntries(path string) ([]bundleEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	header, err := bundler.ReadBundleHeader(file)
	if err != nil {
		return nil, err
	}
	if err := header.Validate(); err != nil {
		return nil, err
	}
	metadata, err := bundler.ReadBundleMetadataHeader(file)
	if err != nil {
		return nil, err
	}
	if err := metadata.Validate(); err != nil {
		return nil, err
	}
	if _, err := io.CopyN(io.Discard, file, int64(metadata.Length)); err != nil {
		return nil, err
	}

	var entries []bundleEntry
	for {
		offset, err := file.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		var magic [8]byte
		if _, err := io.ReadFull(file, magic[:]); err != nil {
			return nil, err
		}
		if _, err := file.Seek(-8, io.SeekCurrent); err != nil {
			return nil, err
		}
		if string(magic[:]) == "GLGYFOOT" {
			footer, err := bundler.ReadBundleFooter(file)
			if err != nil {
				return nil, err
			}
			if err := footer.Validate(); err != nil {
				return nil, err
			}
			if int(footer.Payload.FrameCount) != len(entries) {
				return nil, fmt.Errorf("frame count mismatch in %s", path)
			}
			var extra [1]byte
			if n, err := file.Read(extra[:]); n != 0 || err != io.EOF {
				return nil, fmt.Errorf("trailing data in %s", path)
			}
			return entries, nil
		}
		frame, err := bundler.ReadBundleFrameHeader(file)
		if err != nil {
			return nil, err
		}
		if err := frame.Validate(); err != nil {
			return nil, err
		}
		metadataBytes := make([]byte, frame.MetadataLength)
		if _, err := io.ReadFull(file, metadataBytes); err != nil {
			return nil, err
		}
		var fields struct {
			URL string `cbor:"url"`
		}
		if err := cbor.Unmarshal(metadataBytes, &fields); err != nil {
			return nil, err
		}
		if fields.URL == "" {
			return nil, fmt.Errorf("missing URL in frame at %d", offset)
		}
		if _, err := io.CopyN(io.Discard, file, int64(frame.ContentLength)+4); err != nil {
			return nil, err
		}
		entries = append(entries, bundleEntry{offset, fields.URL})
	}
}

// recoverBundles retries files retained after an upload or indexing failure.
func recoverBundles(uploader bundleUploader, local bool) {
	paths, err := filepath.Glob("bundle_*.bundle")
	if err != nil {
		fmt.Printf("Failed to find bundles for recovery: %v\n", err)
		return
	}
	for _, path := range paths {
		entries, err := readBundleEntries(path)
		if err != nil {
			fmt.Printf("Failed to read retained bundle %s: %v\n", path, err)
			continue
		}
		if err := uploader(context.Background(), path, entries, local); err != nil {
			fmt.Printf("Failed to recover bundle %s: %v\n", path, err)
		}
	}
}

type bundleUploader func(context.Context, string, []bundleEntry, bool) error
