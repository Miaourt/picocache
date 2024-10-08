package main

import (
	"log/slog"
	"net/http"
	"os"
	picocache "picocache/src"

	"github.com/docker/go-units"
)

const envSource = "PICOCACHE_SRC"
const envCachedir = "PICOCACHE_DIR"
const envMaxSize = "PICOCACHE_MAXSIZE"
const envListenTo = "PICOCACHE_LISTENTO"

func main() {
	source := os.Getenv(envSource)
	if source == "" {
		panic(envSource + " is empty")
	}
	cacheDir := os.Getenv(envCachedir)
	if cacheDir == "" {
		panic(envCachedir + " is empty")
	}
	maxSize := os.Getenv(envMaxSize)
	if maxSize == "" {
		panic(envMaxSize + " is empty")
	}
	listenTo := os.Getenv(envListenTo)
	if maxSize == "" {
		panic(envListenTo + " is empty")
	}

	size, err := units.FromHumanSize(maxSize)
	if err != nil {
		panic("can't parse PICOCACHE_MAXSIZE: " + err.Error())
	}

	pcache, err := picocache.NewCache(
		slog.Default().With(slog.String("ident", "main")),
		source,
		cacheDir,
		size,
	)
	if err != nil {
		panic(err)
	}

	http.ListenAndServe(listenTo, pcache)
}
