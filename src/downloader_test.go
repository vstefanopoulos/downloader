package downloader

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// custom test logger (mimics production loggerFunc)
func testLogger(level string, msg string, extras ...any) error {
	fieldBytes, err := json.Marshal(extras)
	if err != nil {
		fieldBytes = []byte{}
	}
	extrasStr := string(fieldBytes)

	fmt.Println(
		strings.ToUpper(level),
		":",
		extrasStr,
	)
	// Uncomment for verbose debugging
	// fmt.Printf("[%s] %s %s\n", level, msg, tags)
	return nil
}

// helper: creates random data for mock responses
func randomData(size int) []byte {
	b := make([]byte, size)
	_, _ = rand.Read(b)
	return b
}

func TestSingleDownload(t *testing.T) {
	data := randomData(1024 * 10) // 10 KB mock file

	// start test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "file.bin")

	d, warnings := NewDownloader(map[string]any{
		"rate-limit":      0,
		"max-downloaders": 2,
		"no-overwrite":    true,
	}, testLogger)

	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	_, err := d.Download(server.URL, dest)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}

	if !bytes.Equal(got, data) {
		t.Fatalf("downloaded data mismatch: got %d bytes, want %d", len(got), len(data))
	}
}

func TestConcurrentDownloads(t *testing.T) {
	fileSize := 1024 * 50 // 50 KB per file
	data := randomData(fileSize)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer server.Close()

	d, _ := NewDownloader(map[string]any{
		"rate-limit":      0,
		"max-downloaders": 3,
		"no-overwrite":    true,
	}, testLogger)

	var wg sync.WaitGroup
	numDownloads := 5
	errs := make(chan error, numDownloads)

	for i := 0; i < numDownloads; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dest := filepath.Join(t.TempDir(), "file_"+hex.EncodeToString([]byte{byte(i)}))
			_, err := d.Download(server.URL, dest)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent download failed: %v", err)
		}
	}

	if d.runningDownloadsCount.Load() != 0 {
		t.Errorf("expected runningDownloadsCount=0, got %d", d.runningDownloadsCount.Load())
	}

	// check for any race-condition issues by running this test with:
	// go test -race
}

// --- RATE LIMITER TESTS ---

func TestRateLimiter_BasicLimit(t *testing.T) {
	rl := rate.NewLimiter(1024, 1024) // 1 KB/s
	start := time.Now()

	// Try reading 2048 bytes; should take at most ~2 seconds
	for i := 0; i < 3; i++ {
		rl.WaitN(t.Context(), 1024)
	}
	elapsed := time.Since(start)

	if elapsed < 2000*time.Millisecond {
		t.Errorf("rate limiter too fast: took %v", elapsed)
	}
}

func TestRateLimiter_NoLimit(t *testing.T) {
	rl := rate.NewLimiter(0, 1024)
	start := time.Now()
	rl.WaitN(t.Context(), 10_000_000)
	if time.Since(start) > 100*time.Millisecond {
		t.Error("rate limiter with 0 bytesPerSec should not block")
	}
}

func TestRateLimiter_ConcurrentWaits(t *testing.T) {
	rl := rate.NewLimiter(1024, 1024)
	start := time.Now()
	var wg sync.WaitGroup

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rl.WaitN(t.Context(), 1024)
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)
	if elapsed < 1800*time.Millisecond {
		t.Errorf("expected at least ~2s delay due to rate limit, got %v", elapsed)
	}
}

func TestRateLimitedReader_ReadsRespectRate(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 2048)
	r := bytes.NewReader(data)
	rl := rate.NewLimiter(1024, 1024)
	rlr := NewRateLimitedReader(r, rl, 1024)

	start := time.Now()
	_, err := io.ReadAll(rlr)
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if elapsed < 1800*time.Millisecond {
		t.Errorf("expected rate-limited read to take ~2s, got %v", elapsed)
	}
}

func TestNoOverwrite(t *testing.T) {
	data := []byte("testdata")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer server.Close()

	d, _ := NewDownloader(map[string]any{
		"rate-limit":      int64(0),
		"max-downloaders": int32(1),
		"no-overwrite":    true,
	}, testLogger)

	dest := filepath.Join(t.TempDir(), "overwrite.bin")
	t.Logf("Destination path: %s", dest)

	// Create existing file
	err := os.WriteFile(dest, data, 0644)
	if err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Verify file exists
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("file should exist: %v", err)
	}
	t.Logf("File exists: size=%d", info.Size())

	// Try to download
	_, err = d.Download(server.URL, dest)
	t.Logf("Error returned: %v", err)

	if err == nil {
		t.Fatalf("expected error due to NoOverwrite, got nil")
	}
}

func TestErrorHandling(t *testing.T) {
	d, _ := NewDownloader(map[string]any{
		"rate-limit":      0,
		"max-downloaders": 1,
		"no-overwrite":    true,
	}, testLogger)

	_, err := d.Download("", "")
	if err == nil {
		t.Fatalf("expected error with empty URL and destPath, got nil")
	}
}

func TestContextCancel(t *testing.T) {
	// Long response, we’ll cancel mid-way
	data := randomData(1024 * 200) // 200 KB

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		for i := 0; i < len(data); i += 1024 {
			select {
			case <-ctx.Done():
				return
			default:
				w.Write(data[i : i+1024])
				time.Sleep(10 * time.Millisecond)
			}
		}
	}))
	defer server.Close()

	d, _ := NewDownloader(map[string]any{
		"rate-limit":      0,
		"max-downloaders": 1,
	}, testLogger)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// run in goroutine to simulate cancellation
	done := make(chan error)
	go func() {
		_, err := d.Download(server.URL, filepath.Join(t.TempDir(), "cancel.bin"))
		done <- err
	}()

	select {
	case <-ctx.Done():
		t.Log("context cancelled before completion (expected)")
	case err := <-done:
		if err != nil {
			t.Logf("download stopped early: %v", err)
		}
	}
}
