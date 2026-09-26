package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/akamensky/argparse"
	"github.com/geulgyeol/html-bundler/bundler"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valyala/gozstd"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	version = "0.1.0"
)

var cdict *gozstd.CDict
var commonMetadata map[string]any
var hostname string

func compressHTML(html string) []byte {
	compressedData := gozstd.CompressDict(nil, []byte(html), cdict)

	return compressedData
}

type QueueItem struct {
	Url       string
	Body      []byte
	Blog      string
	Timestamp uint64
}

var queue = make(chan QueueItem)                        // channel for queueing items
var bundleSizeThreshold uint64 = 2 * 1024 * 1024 * 1024 // 2GiB for default
var bundleTimeThreshold = 3 * time.Hour
var uploadWG sync.WaitGroup

func AddFileToFrame(writer *bundler.BundleWriter, item QueueItem) error {
	metadata := map[string]any{
		"url":       item.Url,
		"blog":      item.Blog,
		"timestamp": item.Timestamp,
	}

	err := writer.WriteFrame(item.Timestamp, metadata, item.Body)
	if err != nil {
		return fmt.Errorf("Failed to write frame: %v", err)
	}

	return nil
}

func ProcessQueueSingle(ctx context.Context) (string, []bundleEntry, error) {
	// create a working file
	now := time.Now()
	startUnix := now.UnixMilli()

	bundleFileName := fmt.Sprintf("bundle_%d_%s.bundle.part", startUnix, hostname)
	bundleFile, err := os.Create(bundleFileName)

	if err != nil {
		return "", nil, fmt.Errorf("Failed to create bundle file: %v", err)
	}

	defer bundleFile.Close()

	metadata := map[string]any{
		"timestamp": startUnix,
	}

	for k, v := range commonMetadata {
		metadata[k] = v
	}

	// open file writer and use it to create a bundle writer
	writer, err := bundler.NewBundle(bundleFile, metadata)
	if err != nil {
		return "", nil, fmt.Errorf("Failed to create bundle writer: %v", err)
	}

	var entries []bundleEntry
	recordFrame := func(item QueueItem) error {
		offset := writer.Length
		if err := AddFileToFrame(writer, item); err != nil {
			return err
		}
		entries = append(entries, bundleEntry{Offset: int64(offset), URL: item.Url})
		return nil
	}

	timeout := time.NewTimer(bundleTimeThreshold)
	defer timeout.Stop()

loop:
	for {
		select {
		// graceful shutdown, draining the queue until it's empty and ignore thresholds
		case <-ctx.Done():
			select {
			case item, ok := <-queue:
				if !ok {
					break loop
				}
				err := recordFrame(item)
				if err != nil {
					fmt.Printf("Failed to add file to frame: %v", err)
				}
			default:
				break loop
			}

		// this is a normal flow
		case item, ok := <-queue:
			if !ok {
				break loop
			}

			err := recordFrame(item)
			if err != nil {
				fmt.Printf("Failed to add file to frame: %v", err)
			}

			if writer.Length >= bundleSizeThreshold {
				break loop
			}
		case <-timeout.C:
			break loop
		}
	}

	// close the bundle writer
	err = writer.Close()
	if err != nil {
		return "", nil, fmt.Errorf("Failed to close bundle writer: %v", err)
	}

	if writer.Count == 0 {
		// no items were added to the bundle, delete the file and return
		err = os.Remove(bundleFileName)
		if err != nil {
			return "", nil, fmt.Errorf("Failed to remove empty bundle file: %v", err)
		}
		return "", nil, nil
	}

	// rename the file to remove the .part extension
	finalBundleFileName := fmt.Sprintf("bundle_%s_%d.bundle", hostname, startUnix)
	err = os.Rename(bundleFileName, finalBundleFileName)
	if err != nil {
		return "", nil, fmt.Errorf("Failed to rename bundle file: %v", err)
	}

	return finalBundleFileName, entries, nil
}

func UploadBundle(context context.Context, client *transfermanager.Client, bucketName string, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("Failed to open bundle file: %v", err)
	}
	defer file.Close()

	// upload the file to S3 using uploader
	_, err = client.UploadObject(context, &transfermanager.UploadObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(filePath),
		Body:   file,
	})
	if err != nil {
		return fmt.Errorf("Failed to upload bundle to S3: %v", err)
	}

	fmt.Printf("Successfully uploaded bundle: %s\n", filePath)

	return nil
}

func ProcessQueue(ctx context.Context, uploader *transfermanager.Client, bucketName string, local bool, pool *pgxpool.Pool) {
	finish := func(ctx context.Context, path string, entries []bundleEntry, local bool) error {
		if !local {
			if err := UploadBundle(ctx, uploader, bucketName, path); err != nil {
				return err
			}
		}
		if err := indexBundle(ctx, pool, path, entries); err != nil {
			return fmt.Errorf("index bundle %s: %w", path, err)
		}
		if !local {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("delete uploaded bundle %s: %w", path, err)
			}
		}
		return nil
	}
	recoverBundles(finish, local)
	shutdownRequested := false

	for {
		if ctx.Err() != nil && !shutdownRequested {
			fmt.Printf("Shutdown requested; flushing remaining queue before exit\n")
			shutdownRequested = true
		}

		bundleFileName, entries, err := ProcessQueueSingle(ctx)
		if err != nil {
			fmt.Printf("Error processing queue: %v\n", err)
			if shutdownRequested {
				uploadWG.Wait()

				return
			}
			continue
		}

		if bundleFileName == "" {
			if shutdownRequested {
				uploadWG.Wait()
				return
			}
			continue
		}

		uploadWG.Add(1)
		go func(bundlePath string, entries []bundleEntry) {
			defer uploadWG.Done()
			if err := finish(context.Background(), bundlePath, entries, local); err != nil {
				fmt.Printf("Error finishing bundle: %v\n", err)
			}
		}(bundleFileName, entries)
	}
}

func parseSize(sizeStr string) (uint64, error) {
	var multiplier uint64 = 1

	if len(sizeStr) > 3 {
		unit := sizeStr[len(sizeStr)-3:]
		switch unit {
		case "KiB":
			multiplier = 1024
			sizeStr = sizeStr[:len(sizeStr)-3]
		case "MiB":
			multiplier = 1024 * 1024
			sizeStr = sizeStr[:len(sizeStr)-3]
		case "GiB":
			multiplier = 1024 * 1024 * 1024
			sizeStr = sizeStr[:len(sizeStr)-3]
		default:
			return 0, fmt.Errorf("unsupported size unit: %s", unit)
		}
	} else if len(sizeStr) > 1 {
		unit := sizeStr[len(sizeStr)-1:]
		switch unit {
		case "B":
			multiplier = 1
			sizeStr = sizeStr[:len(sizeStr)-1]
		default:
			return 0, fmt.Errorf("unsupported size unit: %s", unit)
		}
	}

	var size uint64
	_, err := fmt.Sscanf(sizeStr, "%d", &size)
	if err != nil {
		return 0, fmt.Errorf("failed to parse size: %v", err)
	}

	return size * multiplier, nil
}

func main() {
	gin.SetMode(gin.ReleaseMode)

	parser := argparse.NewParser("html-bundler", "Bundles crawled HTML files into a single efficient bundle, and upload them to S3-compatible object storage.")

	port := parser.Int("p", "port", &argparse.Options{Default: 8080, Help: "Port to run the server on"})
	zstdDictionaryPath := parser.String("z", "zstd-dictionary", &argparse.Options{Default: "./zstd_dict_v2", Help: "Path to Zstd dictionary file"})
	sizeThreshold := parser.String("s", "size-threshold", &argparse.Options{Default: "2GiB", Help: "Size threshold for bundle files (e.g., 2GiB, 500MiB. supported units: B, KiB, MiB, GiB)"})
	timeThreshold := parser.String("t", "time-threshold", &argparse.Options{Default: "3h", Help: "Time threshold for bundle files (e.g., 3h, 30m. supported units: h, m, s)"})
	s3PartSize := parser.Int("b", "s3-part-size", &argparse.Options{Default: 64, Help: "S3 part size in MiB for multipart uploads"})
	s3Concurrency := parser.Int("c", "s3-concurrency", &argparse.Options{Default: 8, Help: "S3 concurrency for multipart uploads"})
	local := parser.Flag("l", "local", &argparse.Options{Help: "Run in local mode without S3 upload, keeping the bundle files in the current directory."})

	err := parser.Parse(os.Args)
	if err != nil {
		panic(err)
	}

	hostname, err = os.Hostname()
	if err != nil {
		fmt.Printf("Failed to get hostname: %v\n", err)
		hostname = "unknown"
	}

	commonMetadata = map[string]any{
		"created_by": fmt.Sprintf("html-bundler v%s (go %s, %s/%s)", version, runtime.Version(), runtime.GOOS, runtime.GOARCH),
		"host":       hostname,
	}

	if *sizeThreshold != "" {
		size, err := parseSize(*sizeThreshold)
		if err != nil {
			panic(fmt.Sprintf("Failed to parse size threshold: %v", err))
		}
		bundleSizeThreshold = size
	}

	if *timeThreshold != "" {
		duration, err := time.ParseDuration(*timeThreshold)
		if err != nil {
			panic(fmt.Sprintf("Failed to parse time threshold: %v", err))
		}
		bundleTimeThreshold = duration
	}

	// Load Zstd dictionary
	dictData, err := os.ReadFile(*zstdDictionaryPath)
	if err != nil {
		panic(fmt.Sprintf("Failed to read Zstd dictionary: %v", err))
	}

	cdict, err = gozstd.NewCDictLevel(dictData, 5)
	if err != nil {
		panic(fmt.Sprintf("Failed to create Zstd dictionary: %v", err))
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		panic("DATABASE_URL must be set")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		panic(fmt.Sprintf("Failed to configure PostgreSQL: %v", err))
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		panic(fmt.Sprintf("Failed to connect to PostgreSQL: %v", err))
	}

	var uploader *transfermanager.Client
	var s3Bucket string

	if !*local {
		// get the required environment variables
		accountId := os.Getenv("S3_ACCOUNT_ID")
		accessKeyId := os.Getenv("S3_ACCESS_KEY_ID")
		accessKeySecret := os.Getenv("S3_SECRET_ACCESS_KEY")
		s3Region := os.Getenv("S3_REGION") // optional, default to "auto"
		s3Endpoint := os.Getenv("S3_ENDPOINT")
		s3Bucket = os.Getenv("S3_BUCKET")

		if accountId == "" || accessKeyId == "" || accessKeySecret == "" || s3Bucket == "" {
			panic("S3_ACCOUNT_ID, S3_ACCESS_KEY_ID, S3_SECRET_ACCESS_KEY, and S3_BUCKET environment variables must be set")
		}

		if s3Region == "" {
			s3Region = "auto"
		}

		cfg, err := config.LoadDefaultConfig(context.TODO(),
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyId, accessKeySecret, "")),
			config.WithRegion(s3Region), // Required by SDK but not used by R2
		)
		if err != nil {
			panic(fmt.Sprintf("Failed to load AWS SDK config: %v", err))
		}

		client := s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(s3Endpoint)
		})

		uploader = transfermanager.New(client, func(u *transfermanager.Options) {
			u.PartSizeBytes = int64(*s3PartSize * 1024 * 1024) // 64MiB per chunk for default
			u.Concurrency = *s3Concurrency                     // 8 concurrent HTTP connections for default
		})
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		ProcessQueue(ctx, uploader, s3Bucket, *local, pool)
	}()

	r := gin.Default()

	r.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	r.POST("/:id", func(c *gin.Context) {
		var body struct {
			Body      string `json:"body"`
			Blog      string `json:"blog"`
			Timestamp uint64 `json:"timestamp"`
		}

		if err := c.BindJSON(&body); err != nil {
			c.JSON(400, gin.H{"error": "Invalid JSON"})
			return
		}

		// compress
		compressedHTML := compressHTML(body.Body)

		// enqueue
		queue <- QueueItem{
			Url:       c.Param("id"),
			Body:      compressedHTML,
			Blog:      body.Blog,
			Timestamp: body.Timestamp,
		}

		c.JSON(200, gin.H{"status": "success"})
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: r,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			fmt.Printf("Server shutdown error: %v\n", err)
		}
	}()

	fmt.Printf("Starting server on port %d\n", *port)

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		panic(fmt.Sprintf("Failed to start server: %v", err))
	}

	<-shutdownDone
}
