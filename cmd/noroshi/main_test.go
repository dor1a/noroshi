package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadLogo(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/logo.png":
			w.Write(png) //nolint:errcheck
		case "/big.png":
			w.Write(make([]byte, maxLogo+1)) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	l, err := loadLogo(srv.URL + "/logo.png")
	if err != nil || string(l.Data) != string(png) || l.ContentType != "image/png" || l.Version == "" {
		t.Fatalf("URL logo: %+v, %v", l, err)
	}
	for path, want := range map[string]string{"/missing.png": "HTTP 404", "/big.png": "larger than 1 MiB"} {
		if _, err := loadLogo(srv.URL + path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", path, want, err)
		}
	}

	file := filepath.Join(t.TempDir(), "logo.svg")
	if err := os.WriteFile(file, []byte("<svg/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l, err := loadLogo(file); err != nil || l.ContentType != "image/svg+xml" {
		t.Fatalf("file logo: %+v, %v", l, err)
	}
}
