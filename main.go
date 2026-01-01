package main

import (
	"log"
	"log/slog"
	"net/http"
	"os"
	picocache "picocache/src"
)

const envSource = "PICOCACHE_SRC"
const envCachedir = "PICOCACHE_DIR"
const envMaxSize = "PICOCACHE_MAXSIZE"
const envListenTo = "PICOCACHE_LISTENTO"

func main() {
	source := os.Getenv(envSource)
	if source == "" {
		log.Fatalln(envSource + " is empty")
	}
	cacheDir := os.Getenv(envCachedir)
	if cacheDir == "" {
		log.Fatalln(envCachedir + " is empty")
	}
	maxSize := os.Getenv(envMaxSize)
	if maxSize == "" {
		log.Fatalln(envMaxSize + " is empty")
	}
	listenTo := os.Getenv(envListenTo)
	if listenTo == "" {
		log.Fatalln(envListenTo + " is empty")
	}

	size, err := picocache.ParseSize(maxSize)
	if err != nil {
		log.Fatalln("can't parse PICOCACHE_MAXSIZE: " + err.Error())
	}

	pcache, err := picocache.NewCache(
		slog.Default().With(slog.String("ident", "main")),
		source,
		cacheDir,
		size,
	)
	if err != nil {
		log.Fatalln(err)
	}

	http.ListenAndServe(listenTo, pcache)
}
