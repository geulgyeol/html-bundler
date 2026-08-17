package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fxamacker/cbor/v2"
	"github.com/geulgyeol/html-bundler/bundler"
	"github.com/valyala/gozstd"
)

func decodeCBOR(data []byte) (any, error) {
	var out any
	if err := cbor.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func formatValue(v any) string {
	if v == nil {
		return "null"
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

type options struct {
	path          string
	frameIndex    int
	dumpMetadata  bool
	dumpHTML      bool
	maxHTMLChars  int
	showRawFrames bool
	dictPath      string
}

func main() {
	var opt options
	flag.StringVar(&opt.path, "file", "", "Path to the bundle file to inspect")
	flag.IntVar(&opt.frameIndex, "frame", -1, "Frame index to inspect; -1 prints all frame summaries")
	flag.BoolVar(&opt.dumpMetadata, "dump-metadata", false, "Dump bundle and frame metadata as CBOR-decoded JSON")
	flag.BoolVar(&opt.dumpHTML, "dump-html", false, "Decompress and print HTML content for the selected frame")
	flag.IntVar(&opt.maxHTMLChars, "max-html-chars", 2000, "Maximum HTML characters to print when dump-html is enabled")
	flag.BoolVar(&opt.showRawFrames, "show-raw-frames", false, "Print raw frame metadata and payload lengths")
	flag.StringVar(&opt.dictPath, "dict", "./zstd_dict_v2", "Path to the Zstandard dictionary file for decompression (optional)")
	flag.Parse()

	if opt.path == "" {
		fmt.Fprintln(os.Stderr, "usage: bundleinspect -file <bundle-file>")
		os.Exit(2)
	}

	data, err := os.ReadFile(opt.path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read bundle: %v\n", err)
		os.Exit(1)
	}

	bundle, err := bundler.ReadBundle(bytes.NewReader(data), false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid bundle: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Bundle valid: %t\n", bundle.Validate() == nil)
	fmt.Printf("Header version: %d\n", bundle.Header.Version)
	fmt.Printf("Dictionary version: %d\n", bundle.Header.DictVersion)
	fmt.Printf("Frame count: %d\n", len(bundle.Frames))

	if opt.dumpMetadata {
		fmt.Println("Bundle metadata:")
		if bundle.Metadata.Content != nil {
			meta, err := decodeCBOR(bundle.Metadata.Content)
			if err != nil {
				fmt.Printf("  (bundle metadata decode failed: %v)\n", err)
			} else {
				fmt.Printf("  %s\n", formatValue(meta))
			}
		}
	}

	if opt.showRawFrames {
		fmt.Println("Raw frame details:")
		for i, frame := range bundle.Frames {
			fmt.Printf("  frame[%d]: ts=%d metadataLen=%d contentLen=%d payloadLen=%d checksum=%d\n",
				i,
				frame.Header.Timestamp,
				frame.Header.MetadataLength,
				frame.Header.ContentLength,
				frame.Header.PayloadLength,
				frame.Payload.Checksum,
			)
		}
	}

	var ddict *gozstd.DDict

	if opt.dictPath != "" {
		dictData, err := os.ReadFile(opt.dictPath)
		if err != nil {
			panic(fmt.Sprintf("Failed to read Zstd dictionary: %v", err))
		}

		ddict, err = gozstd.NewDDict(dictData)
		if err != nil {
			panic(fmt.Sprintf("Failed to create Zstd dictionary: %v", err))
		}
	}

	if opt.frameIndex >= 0 {
		if opt.frameIndex < 0 || opt.frameIndex >= len(bundle.Frames) {
			fmt.Fprintf(os.Stderr, "frame index %d out of range (0..%d)\n", opt.frameIndex, len(bundle.Frames)-1)
			os.Exit(1)
		}
		inspectFrame(bundle, opt.frameIndex, opt, ddict)
		return
	}

	for i := range bundle.Frames {
		inspectFrame(bundle, i, opt, ddict)
	}
}

func inspectFrame(bundle bundler.Bundle, idx int, opt options, ddict *gozstd.DDict) {
	frame := bundle.Frames[idx]
	fmt.Printf("\nFrame[%d]\n", idx)
	fmt.Printf("  timestamp: %d\n", frame.Header.Timestamp)
	fmt.Printf("  metadataLen: %d\n", frame.Header.MetadataLength)
	fmt.Printf("  contentLen: %d\n", frame.Header.ContentLength)
	fmt.Printf("  payloadLen: %d\n", frame.Header.PayloadLength)

	if opt.dumpMetadata {
		meta, err := decodeCBOR(frame.Payload.Metadata)
		if err != nil {
			fmt.Printf("  metadata decode error: %v\n", err)
		} else {
			fmt.Printf("  metadata: %s\n", formatValue(meta))
		}
	}

	if opt.dumpHTML {
		var decompressed []byte
		var err error
		if ddict != nil {
			decompressed, err = gozstd.DecompressDict(nil, frame.Payload.Content, ddict)
		} else {
			decompressed, err = gozstd.Decompress(nil, frame.Payload.Content)
		}
		if err != nil {
			fmt.Printf("  html decode error: %v\n", err)
			return
		}

		html := string(decompressed)
		if len(html) > opt.maxHTMLChars {
			html = html[:opt.maxHTMLChars] + "\n... (truncated)"
		}
		fmt.Printf("  html:\n%s\n", strings.TrimSpace(html))
	}
}
