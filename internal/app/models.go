package app

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// modelCatalog is replaced wholesale by FetchProviderModels and read by every
// request handler, so both sides must go through the accessors below.
var (
	modelMu      sync.RWMutex
	modelCatalog []ModelInfo
)

func setModelCatalog(catalog []ModelInfo) {
	modelMu.Lock()
	modelCatalog = catalog
	modelMu.Unlock()
}

// modelSnapshot returns a copy so callers can iterate without holding the lock
// while a concurrent fetch swaps the catalog out from under them.
func modelSnapshot() []ModelInfo {
	modelMu.RLock()
	defer modelMu.RUnlock()
	return append([]ModelInfo(nil), modelCatalog...)
}

func modelCount() int {
	modelMu.RLock()
	defer modelMu.RUnlock()
	return len(modelCatalog)
}

// modelCatalogRefreshInterval is how often the catalog is re-read from the
// upstream. It used to be fetched only at startup and when an account was
// added, so a long-running gateway kept serving a stale model list.
const modelCatalogRefreshInterval = time.Hour

// FetchProviderModels 从 CC API 拉取模型列表，填充 modelCatalog。
func FetchProviderModels(baseURL, apiKey string) {
	fetchProviderModels(context.Background(), baseURL, apiKey)
}

func fetchProviderModels(ctx context.Context, baseURL, apiKey string) {
	url := baseURL + "/provider/v1/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Printf("[WARN] fetch models: build request failed: %v (using empty catalog)", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := modelHTTPClient.Do(req)
	if err != nil {
		log.Printf("[WARN] fetch models: request failed: %v (using empty catalog)", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("[WARN] fetch models: unexpected status %d (using empty catalog)", resp.StatusCode)
		return
	}

	var list CCProviderModelList
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		log.Printf("[WARN] fetch models: decode response failed: %v (using empty catalog)", err)
		return
	}

	catalog := make([]ModelInfo, 0, len(list.Data))
	for _, m := range list.Data {
		catalog = append(catalog, ModelInfo{
			ID:            m.ID,
			Object:        "model",
			Created:       providerModelCreatedAt,
			OwnedBy:       "commandcode",
			ContextWindow: m.ContextLength,
		})
	}
	setModelCatalog(catalog)
	log.Printf("models: %d loaded from %s", len(catalog), url)
}

// providerModelCreatedAt is a fixed timestamp: the upstream catalog carries no
// creation time, and OpenAI clients only require a stable number.
const providerModelCreatedAt = 1700000000

// modelHTTPClient is shared by every catalog fetch so connections are reused
// instead of re-dialing (and re-TLS-handshaking) on each refresh.
var modelHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: time.Second,
	},
}

// runModelCatalogRefresh keeps the catalog aligned with the upstream. The
// initial fetch runs in the background so a slow or unreachable upstream no
// longer delays the HTTP listener coming up.
func runModelCatalogRefresh(ctx context.Context, cc *CCClient, pool *AccountPool, cfg *Config) {
	refresh := func() {
		acct := pool.Primary()
		if acct == nil {
			return
		}
		fetchProviderModels(ctx, cc.BaseURLValue(), acct.APIKey())
	}
	refresh()

	ticker := time.NewTicker(modelCatalogRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func availableModels() []string {
	catalog := modelSnapshot()
	out := make([]string, 0, len(catalog))
	for _, model := range catalog {
		out = append(out, model.ID)
	}
	return out
}

func isModelExcluded(model string, excludes []string) bool {
	if len(excludes) == 0 {
		return false
	}
	candidates := []string{model}
	if idx := strings.LastIndex(model, "/"); idx >= 0 {
		candidates = append(candidates, model[idx+1:])
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		for _, e := range excludes {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			if strings.HasPrefix(c, e) {
				return true
			}
		}
	}
	return false
}
