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

func TestConcurrentDownloads_Timeout(t *testing.T) {
	// Source server that delays response
	firstRequest := make(chan struct{})
	sourceServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-firstRequest:
		default:
			close(firstRequest)
		}
		// Delay longer than the downloadWaitTimeout (3s)
		time.Sleep(10 * time.Second)
		w.Write([]byte("content"))
	}))
	sourceServer.Config.ReadHeaderTimeout = 0
	sourceServer.Start()
	defer sourceServer.Close()

	cache, err := picocache.NewCache(slog.Default(), sourceServer.URL, t.TempDir(), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(cache)
	defer server.Close()

	// Start first request that will take a long time
	go func() {
		resp, err := server.Client().Get(server.URL + "/file.txt")
		if err == nil {
			resp.Body.Close()
		}
	}()

	// Wait for first request to reach source
	<-firstRequest
	time.Sleep(50 * time.Millisecond) // Ensure download is registered

	// Second concurrent request should timeout (3s), not wait for first to complete (10s)
	start := time.Now()
	resp, err := server.Client().Get(server.URL + "/file.txt")
	elapsed := time.Since(start)

	if err == nil {
		resp.Body.Close()
	}

	// Should complete in ~3s (timeout), not ~10s (waiting for first download)
	if elapsed > 5*time.Second {
		t.Fatalf("request took too long: %v (expected ~3s timeout)", elapsed)
	}
	if elapsed < 2*time.Second {
		t.Fatalf("request completed too quickly: %v (should have waited for timeout)", elapsed)
	}
}

func TestDownloadFile_RetryBehavior(t *testing.T) {
	var requestCount atomic.Int32

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		if count < 3 {
			// Fail first 2 requests
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Third request succeeds
		w.Write([]byte("success"))
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
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Should eventually succeed after retries
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 after retries, got %d", resp.StatusCode)
	}
	if string(body) != "success" {
		t.Errorf("expected body 'success', got '%s'", body)
	}
	if requestCount.Load() != 3 {
		t.Errorf("expected 3 requests (2 failures + 1 success), got %d", requestCount.Load())
	}
}

func TestRebuildCache_TmpFiles(t *testing.T) {
	cacheDir := t.TempDir()

	// Create a .tmp file (simulating incomplete download from previous run)
	tmpFile := filepath.Join(cacheDir, "incomplete.tmp")
	if err := os.WriteFile(tmpFile, []byte("incomplete"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a valid cached file
	validFile := filepath.Join(cacheDir, "valid")
	if err := os.WriteFile(validFile, []byte("valid"), 0644); err != nil {
		t.Fatal(err)
	}

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("from source"))
	}))
	defer sourceServer.Close()

	// Create cache - should clean up .tmp files
	_, err := picocache.NewCache(slog.Default(), sourceServer.URL, cacheDir, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	// .tmp file should be removed
	if _, err := os.Stat(tmpFile); !os.IsNotExist(err) {
		t.Error("expected .tmp file to be removed during rebuild")
	}

	// Valid file should still exist
	if _, err := os.Stat(validFile); err != nil {
		t.Error("expected valid file to still exist")
	}
}

func TestCleanupOldEntries_RemoveError(t *testing.T) {
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("0123456789")) // 10 bytes
	}))
	defer sourceServer.Close()

	cacheDir := t.TempDir()
	// Cache with max size of 15 bytes (fits 1 file but not 2)
	cache, err := picocache.NewCache(slog.Default(), sourceServer.URL, cacheDir, 15)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(cache)
	defer server.Close()

	// Cache first file
	resp1, err := server.Client().Get(server.URL + "/file1.txt")
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp1.Body)
	resp1.Body.Close()

	// Manually delete the cached file to simulate fs error during cleanup
	entries, _ := os.ReadDir(cacheDir)
	for _, e := range entries {
		os.Remove(filepath.Join(cacheDir, e.Name()))
	}

	// Request second file - triggers cleanup which should handle missing file gracefully
	resp2, err := server.Client().Get(server.URL + "/file2.txt")
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp2.Body)
	resp2.Body.Close()

	// Second file should succeed despite cleanup error
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp2.StatusCode)
	}
}
