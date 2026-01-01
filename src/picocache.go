package picocache

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	downloadRetries      = 3
	downloadWaitInterval = 100 * time.Millisecond
	downloadWaitTimeout  = 3 * time.Second
	cacheMaxAge          = 604800 // 7 days in seconds
)

type cacheEntry struct {
	filename string
	size     int64
	lastUsed time.Time
}

type PicoCache struct {
	log          *slog.Logger
	source       string
	cacheDir     string
	maxCacheSize int64
	entries      sync.Map
	totalSize    atomic.Int64
	downloading  sync.Map   // Track ongoing downloads
	cleanupMutex sync.Mutex // Prevent concurrent cleanups
}

func NewCache(logger *slog.Logger, source string, cacheDir string, maxCacheSize int64) (*PicoCache, error) {
	cache := &PicoCache{
		log:          logger,
		source:       source,
		cacheDir:     cacheDir,
		maxCacheSize: maxCacheSize,
		entries:      sync.Map{},
		downloading:  sync.Map{},
	}

	cache.log.Info("Creating cache folder if it doesn't exists...")
	if err := os.Mkdir(cacheDir, 0755); err != nil && !strings.Contains(err.Error(), "file exists") {
		return nil, err
	}

	cache.log.Info("Rebuilding index with already existing cache entries...")
	if err := cache.rebuildCache(); err != nil {
		return nil, err
	}
	cache.log.Info("All good, starting cache!")

	return cache, nil
}

const crockfordBase32 string = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var b32 = base32.NewEncoding(crockfordBase32).WithPadding(base32.NoPadding)

func (c *PicoCache) getCacheFilename(r *http.Request) string {
	hash := sha256.Sum256([]byte(r.URL.Path))
	return filepath.Join(c.cacheDir, b32.EncodeToString(hash[:]))
}

func (c *PicoCache) cleanupOldEntries() {
	if !c.cleanupMutex.TryLock() {
		return
	}
	defer c.cleanupMutex.Unlock()

	if c.totalSize.Load() < c.maxCacheSize {
		return
	}

	c.log.Info("Starting cache cleanup...")

	type entryWithURL struct {
		filename string
		entry    *cacheEntry
	}

	sortedEntries := []*entryWithURL{}
	c.entries.Range(func(key, value any) bool {
		sortedEntries = append(sortedEntries, &entryWithURL{key.(string), value.(*cacheEntry)})
		return true
	})

	slices.SortFunc(sortedEntries, func(a, b *entryWithURL) int {
		if a.entry.lastUsed.Before(b.entry.lastUsed) {
			return -1
		}
		return +1
	})

	removedCount := 0
	removedSize := int64(0)
	for _, e := range sortedEntries {
		os.Remove(e.entry.filename)
		c.entries.Delete(e.filename)
		removedSize += e.entry.size
		removedCount++
		if c.totalSize.Add(-e.entry.size) <= c.maxCacheSize {
			break
		}
	}

	c.log.Info("Cache cleanup completed",
		slog.Int("removed_files", removedCount),
		slog.Int64("removed_size", removedSize),
		slog.Int64("current_size", c.totalSize.Load()))
}

func (c *PicoCache) rebuildCache() error {
	c.totalSize.Store(0)

	err := filepath.WalkDir(c.cacheDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		if strings.HasSuffix(path, ".tmp") {
			os.Remove(path)
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		c.entries.Store(path, &cacheEntry{
			filename: path,
			size:     info.Size(),
			lastUsed: info.ModTime(),
		})
		c.totalSize.Add(info.Size())
		return nil
	})

	if err != nil {
		return err
	}

	go c.cleanupOldEntries()

	return nil
}

func (c *PicoCache) downloadFile(url string, cacheFile string) (*cacheEntry, error) {
	// Check if download is already in progress
	if _, exists := c.downloading.LoadOrStore(cacheFile, true); exists {
		// Wait for other download to complete with timeout
		deadline := time.Now().Add(downloadWaitTimeout)
		for time.Now().Before(deadline) {
			if entry, ok := c.entries.Load(cacheFile); ok {
				c.downloading.Delete(cacheFile)
				return entry.(*cacheEntry), nil
			}
			time.Sleep(downloadWaitInterval)
		}
		return nil, fmt.Errorf("timeout waiting for concurrent download")
	}
	defer c.downloading.Delete(cacheFile)

	for attempts := 0; attempts < downloadRetries; attempts++ {
		resp, err := http.Get(url)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			continue
		}

		tempFile := cacheFile + ".tmp"
		file, err := os.Create(tempFile)
		if err != nil {
			return nil, err
		}

		n, err := io.Copy(file, resp.Body)
		file.Close()

		if err != nil || n != resp.ContentLength {
			os.Remove(tempFile)
			continue
		}

		err = os.Rename(tempFile, cacheFile)
		if err != nil {
			os.Remove(tempFile)
			return nil, err
		}

		entry := &cacheEntry{
			filename: cacheFile,
			size:     resp.ContentLength,
			lastUsed: time.Now(),
		}
		c.entries.Store(cacheFile, entry)

		c.totalSize.Add(resp.ContentLength)

		go c.cleanupOldEntries()

		return entry, nil
	}

	return nil, fmt.Errorf("failed to download file after %d attempts", downloadRetries)
}

func (c *PicoCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/favicon.ico" || r.URL.Path == "/" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	log := c.log.With(slog.String("url", r.URL.Path))
	cacheFile := c.getCacheFilename(r)

	header := w.Header()
	header.Set("X-Cache", "MISS")
	header.Set("Cache-Control", fmt.Sprintf("public, max-age=%d, immutable", cacheMaxAge))
	header.Set("Content-Type", mime.TypeByExtension(filepath.Ext(r.URL.Path)))
	header.Set("Accept-Ranges", "bytes")
	etag := filepath.Base(cacheFile)
	header.Set("ETag", etag)

	if match := r.Header.Get("If-None-Match"); match != "" &&
		strings.EqualFold(match, etag) {

		w.WriteHeader(http.StatusNotModified)
		return
	}

	var entry *cacheEntry
	if e, ok := c.entries.Load(cacheFile); ok {
		entry = e.(*cacheEntry)
		header.Set("X-Cache", "HIT")
	} else {
		var err error
		entry, err = c.downloadFile(c.source+r.URL.Path, cacheFile)
		if err != nil {
			log.Error("Failed to download file", slog.String("err", err.Error()))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	file, err := os.Open(entry.filename)
	if err != nil {
		log.Error("Failed to open cached file", slog.String("err", err.Error()))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer file.Close()

	var fileReader io.Reader
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		// Handle ranged request
		rang, err := parseRange(rangeHeader, entry.size)
		if err != nil {
			log.Debug("Error parsing range", slog.String("err", err.Error()), slog.String("rangeHeader", rangeHeader))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}

		if _, err := file.Seek(rang.start, io.SeekStart); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rang.start, rang.end, entry.size))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", rang.end-rang.start+1))
		w.WriteHeader(http.StatusPartialContent)

		fileReader = io.LimitReader(file, rang.end-rang.start+1)
	} else {
		fileReader = file
	}

	if _, err := io.Copy(&writerClientError{w}, fileReader); err != nil &&
		!(errors.Is(err, errClientError) && (errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET))) {

		log.Error("Failed to stream file", slog.String("err", err.Error()))
		return
	}

	// Update last used time
	now := time.Now()
	os.Chtimes(entry.filename, now, now)
	entry.lastUsed = now
}

var errClientError = errors.New("client error")

type writerClientError struct {
	http.ResponseWriter
}

func (rcr *writerClientError) Write(p []byte) (n int, err error) {
	n, err = rcr.ResponseWriter.Write(p)
	if err != nil {
		err = errors.Join(errClientError, err)
	}
	return
}

type httpRange struct {
	start, end int64
}

// parseRange parses a Range header string as per RFC 7233.
func parseRange(s string, size int64) (*httpRange, error) {
	ra, exist := strings.CutPrefix(s, "bytes=")
	if !exist {
		return nil, fmt.Errorf("invalid range prefix")
	}

	start, end, found := strings.Cut(ra, "-")
	if !found {
		return nil, fmt.Errorf("invalid range: no hyphen found")
	}

	r := &httpRange{}

	if start == "" {
		// Suffix range: -N means last N bytes
		i, err := strconv.ParseInt(end, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid range suffix format")
		}
		if i > size {
			i = size
		}
		r.start = size - i
		r.end = size - 1
	} else {
		// Normal range: N- or N-M
		i, err := strconv.ParseInt(start, 10, 64)
		if err != nil || i < 0 {
			return nil, fmt.Errorf("invalid range start format")
		}
		r.start = i

		if end == "" {
			// No end specified, go to end of file
			r.end = size - 1
		} else {
			i, err := strconv.ParseInt(end, 10, 64)
			if err != nil || r.start > i {
				return nil, fmt.Errorf("invalid range end format")
			}
			r.end = i
		}
	}

	// Check if range is valid
	if r.start >= size {
		return nil, fmt.Errorf("invalid range: start position exceeds content size")
	}
	if r.end >= size {
		r.end = size - 1
	}

	return r, nil
}
