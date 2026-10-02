// Package web serves the embedded single-file admin interface. The same
// index.html is committed at internal/web/index.html so it can also be opened
// directly in a browser and pointed at a running gateway.
package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

//go:embed index.html
var indexHTML []byte

// The console is one ~85 KB HTML document. It used to be sent with
// Cache-Control: no-store on every load, so each dashboard refresh re-sent the
// whole file. It is embedded and therefore immutable for a given binary, so an
// ETag is enough to turn repeat loads into 304s, and gzip cuts the first load
// to roughly a quarter.

var (
	assetOnce sync.Once
	assetETag string
	assetGzip []byte
)

func initAssets() {
	assetOnce.Do(func() {
		sum := sha256.Sum256(indexHTML)
		assetETag = `"` + hex.EncodeToString(sum[:16]) + `"`
		var buf bytes.Buffer
		zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if err != nil {
			return
		}
		if _, err := zw.Write(indexHTML); err != nil {
			return
		}
		if err := zw.Close(); err != nil {
			return
		}
		assetGzip = buf.Bytes()
	})
}

// Handler serves the UI under /webui. Only exact index paths return HTML so
// stray API typos keep their JSON 404s.
func Handler() http.HandlerFunc {
	initAssets()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/webui", "/webui/", "/webui/index.html":
			// ok
		default:
			http.NotFound(w, r)
			return
		}

		// The document changes only when the binary does, so revalidate instead
		// of re-downloading: a matching ETag short-circuits to 304.
		w.Header().Set("ETag", assetETag)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if etagMatches(r.Header.Get("If-None-Match"), assetETag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		if len(assetGzip) > 0 && acceptsGzip(r.Header.Get("Accept-Encoding")) {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Length", strconv.Itoa(len(assetGzip)))
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			_, _ = w.Write(assetGzip)
			return
		}

		w.Header().Set("Content-Length", strconv.Itoa(len(indexHTML)))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(indexHTML)
	}
}

// etagMatches reports whether an If-None-Match header covers our ETag. "*"
// matches anything; weak validators (W/"x") compare equal by value here, which
// is what a byte-identical embedded asset warrants.
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		encoding := strings.TrimSpace(part)
		if i := strings.IndexByte(encoding, ';'); i >= 0 {
			// Honour an explicit q=0 rejection.
			params := encoding[i+1:]
			encoding = strings.TrimSpace(encoding[:i])
			if strings.Contains(params, "q=0") && !strings.Contains(params, "q=0.") {
				continue
			}
		}
		if strings.EqualFold(encoding, "gzip") || encoding == "*" {
			return true
		}
	}
	return false
}
