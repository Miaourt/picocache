package picocache_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	picocache "picocache/src"
	"testing"
)

func TestPicocache(t *testing.T) {
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		_, err := w.Write([]byte("Yay"))
		if err != nil {
			t.Fatal(err)
		}

		b, err := httputil.DumpRequest(r, true)
		if err != nil {
			t.Fatal(err)
		}

		t.Log("SRC: got request:\n" + string(b))
	}))

	cacheDir := t.TempDir()

	cache, err := picocache.NewCache(slog.Default(), sourceServer.URL, cacheDir, 900)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(cache)

	client := server.Client()

	resp, err := client.Get(server.URL + "/hi?feur")
	if err != nil {
		t.Fatal(err)
	}

	b, err := httputil.DumpResponse(resp, true)
	if err != nil {
		t.Fatal(err)
	}

	t.Log("Client:\n" + string(b))
	t.Fail()
}
