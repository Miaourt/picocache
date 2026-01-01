package picocache_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	picocache "picocache/src"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPicocache_BasicCacheMiss(t *testing.T) {
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello from source"))
	}))
	defer sourceServer.Close()

	cache, err := picocache.NewCache(slog.Default(), sourceServer.URL, t.TempDir(), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(cache)
	defer server.Close()

	resp, err := server.Client().Get(server.URL + "/test.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "Hello from source" {
		t.Errorf("expected body 'Hello from source', got '%s'", body)
	}

	if resp.Header.Get("X-Cache") != "MISS" {
		t.Errorf("expected X-Cache: MISS, got %s", resp.Header.Get("X-Cache"))
	}

	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Errorf("expected Accept-Ranges: bytes, got %s", resp.Header.Get("Accept-Ranges"))
	}

	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header to be set")
	}
}

func TestServeHTTP_MethodNotAllowed(t *testing.T) {
	cache, err := picocache.NewCache(slog.Default(), "http://localhost", t.TempDir(), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(cache)
	defer server.Close()

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/test.txt", nil)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", resp.StatusCode)
	}
}

func TestServeHTTP_FilteredPaths(t *testing.T) {
	cache, err := picocache.NewCache(slog.Default(), "http://localhost", t.TempDir(), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(cache)
	defer server.Close()

	tests := []string{"/", "/favicon.ico"}
	for _, path := range tests {
		resp, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("path %s: expected status 404, got %d", path, resp.StatusCode)
		}
	}
}

func TestServeHTTP_CacheHit(t *testing.T) {
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("cached content"))
	}))
	defer sourceServer.Close()

	cache, err := picocache.NewCache(slog.Default(), sourceServer.URL, t.TempDir(), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(cache)
	defer server.Close()

	// First request - should be MISS
	resp1, err := server.Client().Get(server.URL + "/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp1.Body)
	resp1.Body.Close()

	if resp1.Header.Get("X-Cache") != "MISS" {
		t.Errorf("first request: expected X-Cache: MISS, got %s", resp1.Header.Get("X-Cache"))
	}

	// Second request - should be HIT
	resp2, err := server.Client().Get(server.URL + "/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if resp2.Header.Get("X-Cache") != "HIT" {
		t.Errorf("second request: expected X-Cache: HIT, got %s", resp2.Header.Get("X-Cache"))
	}

	if string(body) != "cached content" {
		t.Errorf("expected body 'cached content', got '%s'", body)
	}
}

func TestServeHTTP_ETagNotModified(t *testing.T) {
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("content"))
	}))
	defer sourceServer.Close()

	cache, err := picocache.NewCache(slog.Default(), sourceServer.URL, t.TempDir(), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(cache)
	defer server.Close()

	// First request to get the ETag
	resp1, err := server.Client().Get(server.URL + "/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp1.Body)
	resp1.Body.Close()

	etag := resp1.Header.Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag in response")
	}

	// Second request with If-None-Match
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/file.txt", nil)
	req.Header.Set("If-None-Match", etag)

	resp2, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusNotModified {
		t.Errorf("expected status 304, got %d", resp2.StatusCode)
	}
}

func TestServeHTTP_RangeRequests(t *testing.T) {
	content := "0123456789ABCDEF" // 16 bytes
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(content))
	}))
	defer sourceServer.Close()

	cache, err := picocache.NewCache(slog.Default(), sourceServer.URL, t.TempDir(), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(cache)
	defer server.Close()

	// First request to cache the file
	resp, _ := server.Client().Get(server.URL + "/file.txt")
	io.ReadAll(resp.Body)
	resp.Body.Close()

	tests := []struct {
		name        string
		rangeHeader string
		wantStatus  int
		wantBody    string
		wantRange   string
	}{
		{"normal range", "bytes=0-4", 206, "01234", "bytes 0-4/16"},
		{"open-ended", "bytes=10-", 206, "ABCDEF", "bytes 10-15/16"},
		{"suffix range", "bytes=-5", 206, "BCDEF", "bytes 11-15/16"},
		{"invalid prefix", "chars=0-5", 416, "", ""},
		{"start exceeds size", "bytes=100-200", 416, "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, server.URL+"/file.txt", nil)
			req.Header.Set("Range", tt.rangeHeader)

			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("expected status %d, got %d", tt.wantStatus, resp.StatusCode)
			}

			if tt.wantStatus == 206 {
				if string(body) != tt.wantBody {
					t.Errorf("expected body '%s', got '%s'", tt.wantBody, body)
				}
				if resp.Header.Get("Content-Range") != tt.wantRange {
					t.Errorf("expected Content-Range '%s', got '%s'", tt.wantRange, resp.Header.Get("Content-Range"))
				}
			}
		})
	}
}

func TestServeHTTP_DownloadFailure(t *testing.T) {
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sourceServer.Close()

	cache, err := picocache.NewCache(slog.Default(), sourceServer.URL, t.TempDir(), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(cache)
	defer server.Close()

	resp, err := server.Client().Get(server.URL + "/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", resp.StatusCode)
	}
}

func TestCleanupOldEntries(t *testing.T) {
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return 10 bytes for each request
		w.Write([]byte("0123456789"))
	}))
	defer sourceServer.Close()

	// Cache with max size of 25 bytes (fits 2 files but not 3)
	cache, err := picocache.NewCache(slog.Default(), sourceServer.URL, t.TempDir(), 25)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(cache)
	defer server.Close()

	// Request 3 files - third should trigger cleanup
	for i := 1; i <= 3; i++ {
		resp, err := server.Client().Get(server.URL + "/file" + string(rune('0'+i)) + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
		time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	}

	// Give cleanup goroutine time to run
	time.Sleep(100 * time.Millisecond)

	// Request file1 again - should be MISS because it was evicted
	resp, err := server.Client().Get(server.URL + "/file1.txt")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.Header.Get("X-Cache") != "MISS" {
		t.Errorf("expected X-Cache: MISS (file was evicted), got %s", resp.Header.Get("X-Cache"))
	}
}

func TestRebuildCache(t *testing.T) {
	cacheDir := t.TempDir()

	// Create pre-existing files in the cache directory
	for i := 1; i <= 3; i++ {
		filename := filepath.Join(cacheDir, "testfile"+string(rune('0'+i)))
		if err := os.WriteFile(filename, []byte("cached"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("source server should not be called for pre-cached files")
		w.Write([]byte("from source"))
	}))
	defer sourceServer.Close()

	// Create cache - it should index existing files
	_, err := picocache.NewCache(slog.Default(), sourceServer.URL, cacheDir, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	// Verify files still exist (weren't deleted)
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 3 {
		t.Errorf("expected 3 files in cache, got %d", len(entries))
	}
}

func TestConcurrentDownloads(t *testing.T) {
	var downloadCount atomic.Int32

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloadCount.Add(1)
		time.Sleep(50 * time.Millisecond) // Simulate slow download
		w.Write([]byte("content"))
	}))
	defer sourceServer.Close()

	cache, err := picocache.NewCache(slog.Default(), sourceServer.URL, t.TempDir(), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(cache)
	defer server.Close()

	// Send 5 concurrent requests for the same file
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Go(func() {
			resp, err := server.Client().Get(server.URL + "/file.txt")
			if err != nil {
				t.Error(err)
				return
			}
			io.ReadAll(resp.Body)
			resp.Body.Close()
		})
	}
	wg.Wait()

	// Should only have downloaded once
	if downloadCount.Load() != 1 {
		t.Errorf("expected 1 download, got %d", downloadCount.Load())
	}
}
