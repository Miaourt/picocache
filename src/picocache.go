package picocache

import (
	"crypto/sha256"
	"encoding/base32"
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
	"time"
)

type cacheEntry struct {
	filename    string
	size        int64
	lastUsed    time.Time
	beingCached sync.RWMutex
}

type PicoCache struct {
	log          *slog.Logger
	source       string
	cacheDir     string
	maxCacheSize int64
	entries      sync.Map
	totalSize    atomic.Int64
}

func NewCache(logger *slog.Logger, source string, cacheDir string, maxCacheSize int64) (*PicoCache, error) {
	cache := &PicoCache{
		log:          logger,
		source:       source,
		cacheDir:     cacheDir,
		maxCacheSize: maxCacheSize,
		entries:      sync.Map{},
	}

	cache.log.Info("Creating cache folder if it doesn't exists...")
	if err := os.Mkdir(cacheDir, 0755); err != nil && !strings.Contains(err.Error(), "file exists") {
		return nil, err
	}

	cache.log.Info("Rebuilding index with already exisiting cache entries...")
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

	for _, e := range sortedEntries {
		os.Remove(e.entry.filename)
		c.entries.Delete(e.filename)
		if c.totalSize.Add(-e.entry.size) <= c.maxCacheSize {
			break
		}
	}
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

	if c.totalSize.Load() > c.maxCacheSize {
		c.cleanupOldEntries()
	}

	return nil
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
	header.Set("Cache-Control", "public, max-age=604800, immutable")
	header.Set("Content-Type", mime.TypeByExtension(filepath.Ext(r.URL.Path)))
	header.Set("ETag", filepath.Base(cacheFile))

	if r.Header.Get("If-None-Match") == header.Get("ETag") {
		log.Info("ETag is the same as If-None-Match, returning 304")
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if e, ok := c.entries.Load(cacheFile); ok {
		entry := e.(*cacheEntry)

		// Wait for the file to be fully cached before sending it
		entry.beingCached.RLock()
		defer entry.beingCached.RUnlock()

		cachedFile, err := os.Open(entry.filename)
		if err == nil {
			log.Info("Cache hit")
			header.Set("X-Cache", "HIT")
			header.Set("Content-Length", strconv.FormatInt(entry.size, 10))
			defer cachedFile.Close()
			_, err := io.Copy(w, cachedFile)
			if err == nil {
				go func() {
					now := time.Now()
					os.Chtimes(entry.filename, now, now)
					entry.lastUsed = now
				}()
				return
			}
			log.Error("Failed to stream cached file", slog.String("err", err.Error()))
		} else {
			log.Error("Failed to open cached file", slog.String("err", err.Error()))
		}
	}

	log.Info("Cache miss")
	resp, err := http.Get(c.source + r.URL.Path)
	if err != nil {
		log.Error("Error from source", slog.Int("statuscode", resp.StatusCode))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Error("Source not returning 200", slog.Int("statuscode", resp.StatusCode))
		w.WriteHeader(http.StatusNotFound)
		return
	}

	header.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))

	newEntry := &cacheEntry{
		filename:    cacheFile,
		size:        resp.ContentLength,
		lastUsed:    time.Now(),
		beingCached: sync.RWMutex{},
	}

	// Write lock so requests trying to read the
	// same file will wait for it to be cached
	newEntry.beingCached.Lock()
	defer newEntry.beingCached.Unlock()

	c.entries.Store(cacheFile, newEntry)

	newFile, err := os.Create(cacheFile)
	if err != nil {
		log.Error("Error creating cache file", slog.String("err", err.Error()))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer newFile.Close()

	if n, err := io.Copy(io.MultiWriter(newFile, w), resp.Body); err != nil || int64(n) != resp.ContentLength {
		log.Error("Error serving request", slog.String("err", err.Error()), slog.Int64("expectedSize", resp.ContentLength), slog.Int64("transferedSize", n))
		w.WriteHeader(http.StatusInternalServerError)
		os.Remove(cacheFile)
		return
	}

	if c.totalSize.Add(resp.ContentLength) > c.maxCacheSize {
		c.cleanupOldEntries()
	}
}
