package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type requestItem struct {
	Body      string `json:"body"`
	Blog      string `json:"blog"`
	Timestamp uint64 `json:"timestamp"`
}

func syntheticHTML(size int) string {
	if size <= 0 {
		return "<html><body></body></html>"
	}
	return "<html><head><title>synthetic</title></head><body>" + strings.Repeat("x", size) + "</body></html>"
}

func buildSingleRequest(bodySize int) ([]byte, error) {
	payload := requestItem{
		Body:      syntheticHTML(bodySize),
		Blog:      "benchmark",
		Timestamp: uint64(time.Now().UnixMilli()),
	}
	return json.Marshal(payload)
}

func runBenchmark(baseURL string, requests int, concurrency int, batchSize int, bodySize int, timeout time.Duration, verbose bool) {
	if requests <= 0 {
		fmt.Println("requests must be > 0")
		return
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	if batchSize <= 0 {
		batchSize = 1
	}

	jobs := make(chan int, requests)
	for i := 0; i < requests; i++ {
		jobs <- i
	}
	close(jobs)

	client := &http.Client{Timeout: timeout}
	var success atomic.Int64
	var failed atomic.Int64
	var totalLatency atomic.Int64
	var statsMu sync.Mutex
	var minLatencyNs int64
	var maxLatencyNs int64
	var hasMin bool

	if verbose {
		fmt.Printf("Starting %d requests with concurrency=%d\n", requests, concurrency)
	}

	start := time.Now()
	var wg sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for jobID := range jobs {
				requestStart := time.Now()
				var payload []byte
				var err error
				var url string

				payload, err = buildSingleRequest(bodySize)
				if err != nil {
					failed.Add(1)
					continue
				}
				url = fmt.Sprintf("%s/%d", strings.TrimRight(baseURL, "/"), jobID)

				req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
				if err != nil {
					failed.Add(1)
					continue
				}
				req.Header.Set("Content-Type", "application/json")

				resp, err := client.Do(req)
				latency := time.Since(requestStart)
				totalLatency.Add(int64(latency))
				if err != nil {
					failed.Add(1)
					continue
				}
				_ = resp.Body.Close()

				_, _ = io.Copy(io.Discard, resp.Body)
				if resp.StatusCode == http.StatusOK {
					success.Add(1)
				} else {
					failed.Add(1)
				}

				statsMu.Lock()
				if !hasMin || int64(latency) < minLatencyNs {
					minLatencyNs = int64(latency)
					hasMin = true
				}
				if int64(latency) > maxLatencyNs {
					maxLatencyNs = int64(latency)
				}
				statsMu.Unlock()

				if verbose {
					fmt.Printf("done worker=%d job=%d status=%d latency=%s\n", worker, jobID, resp.StatusCode, latency)
				}
			}
		}(worker)
	}
	wg.Wait()

	elapsed := time.Since(start)
	successCount := success.Load()
	failedCount := failed.Load()
	avgLatency := time.Duration(0)
	if successCount > 0 {
		avgLatency = time.Duration(totalLatency.Load() / successCount)
	}
	throughput := 0.0
	if elapsed.Seconds() > 0 {
		throughput = float64(successCount) / elapsed.Seconds()
	}

	fmt.Println("Benchmark summary")
	fmt.Printf("  target: %s\n", baseURL)
	fmt.Printf("  requests: %d\n", requests)
	fmt.Printf("  concurrency: %d\n", concurrency)
	fmt.Printf("  batch size: %d\n", batchSize)
	fmt.Printf("  body size: %d bytes\n", bodySize)
	fmt.Printf("  success: %d\n", successCount)
	fmt.Printf("  failed: %d\n", failedCount)
	fmt.Printf("  runtime: %s\n", elapsed.Round(time.Millisecond))
	statsMu.Lock()
	minLatency := time.Duration(minLatencyNs)
	maxLatency := time.Duration(maxLatencyNs)
	statsMu.Unlock()

	fmt.Printf("  avg latency: %s\n", avgLatency)
	fmt.Printf("  min latency: %s\n", minLatency)
	fmt.Printf("  max latency: %s\n", maxLatency)
	fmt.Printf("  throughput: %.2f req/s\n", throughput)
	fmt.Printf("  throughput by items: %.2f items/s\n", throughput*float64(batchSize))
}

func main() {
	baseURL := flag.String("url", "http://127.0.0.1:8080", "Base URL of the html-bundler server")
	requests := flag.Int("requests", 100, "Number of synthetic requests to send")
	concurrency := flag.Int("concurrency", 10, "Concurrent worker count")
	batchSize := flag.Int("batch-size", 10, "Number of items per batch request when mode=batch")
	bodySize := flag.Int("body-size", 1024, "Approximate synthetic HTML body size in bytes per item")
	timeout := flag.Duration("timeout", 20*time.Second, "Per-request HTTP timeout")
	verbose := flag.Bool("verbose", false, "Print per-request progress; default is summary only")
	flag.Parse()

	fmt.Printf("Sending synthetic benchmark traffic to %s\n", *baseURL)
	runBenchmark(*baseURL, *requests, *concurrency, *batchSize, *bodySize, *timeout, *verbose)
}
