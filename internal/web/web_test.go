package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesIndexAtWebuiPaths(t *testing.T) {
	for _, path := range []string{"/webui", "/webui/", "/webui/index.html"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		Handler()(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, rec.Code)
		}
		if got := rec.Header().Get("ETag"); got == "" {
			t.Fatalf("%s: missing ETag", path)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s: Cache-Control = %q, want no-cache", path, got)
		}
		if body := rec.Body.String(); !strings.Contains(body, "<html") {
			t.Fatalf("%s: body does not look like the console (len=%d)", path, len(body))
		}
	}
}

func TestHandlerReturns304OnMatchingETag(t *testing.T) {
	first := httptest.NewRecorder()
	Handler()(first, httptest.NewRequest(http.MethodGet, "/webui", nil))
	etag := first.Header().Get("ETag")

	req := httptest.NewRequest(http.MethodGet, "/webui", nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	Handler()(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("304 carried a body of %d bytes", rec.Body.Len())
	}
}

func TestHandlerServesGzipWhenAccepted(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/webui", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	Handler()(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip body: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	if !strings.Contains(string(body), "<html") {
		t.Fatalf("decompressed body does not look like the console (len=%d)", len(body))
	}
	if len(rec.Body.Bytes()) >= len(body) {
		t.Fatalf("gzip body (%d) is not smaller than plain (%d)", len(rec.Body.Bytes()), len(body))
	}
}

func TestHandlerNotFoundOutsideWebui(t *testing.T) {
	for _, path := range []string{"/", "/v1/models", "/webui/other.html"} {
		rec := httptest.NewRecorder()
		Handler()(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", path, rec.Code)
		}
	}
}

func TestAcceptsGzipHonoursQZero(t *testing.T) {
	cases := map[string]bool{
		"":                        false,
		"gzip":                    true,
		"deflate, gzip;q=0.8":     true,
		"gzip;q=0":                false,
		"identity, *;q=0.5":       true,
		"br, deflate":             false,
		"GZIP":                    true,
		"deflate;q=0, gzip;q=0.1": true,
	}
	for header, want := range cases {
		if got := acceptsGzip(header); got != want {
			t.Fatalf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestEtagMatches(t *testing.T) {
	const etag = `"abc123"`
	cases := map[string]bool{
		"":                  false,
		`"abc123"`:          true,
		"*":                 true,
		`W/"abc123"`:        true,
		`"other"`:           false,
		`"other", "abc123"`: true,
	}
	for header, want := range cases {
		if got := etagMatches(header, etag); got != want {
			t.Fatalf("etagMatches(%q) = %v, want %v", header, got, want)
		}
	}
}
