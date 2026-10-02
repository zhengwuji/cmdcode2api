package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// clientKeyIdent is the immutable identity half of a ClientKey. Like
// Account.ident it is published once, so authMiddleware can read the ID and the
// raw key without contending on the per-key lock that guards name/enabled.
type clientKeyIdent struct {
	id  string
	key string
}

// ClientKey is one local bearer key that callers use to reach this gateway.
// Keys are surfaced to the admin in full — the admin password already grants
// full config control, so masking them would only add friction.
//
// Locking contract: ident is immutable and read lock-free; mu (a RWMutex)
// guards only name/enabled/lastUsedAt, so authMiddleware's per-request
// IsEnabled check takes a shared lock while the admin API renames or toggles.
type ClientKey struct {
	ident      atomic.Pointer[clientKeyIdent]
	mu         sync.RWMutex
	name       string
	enabled    bool
	lastUsedAt time.Time
}

func newClientKey(name, key string, enabled bool) *ClientKey {
	k := &ClientKey{name: name, enabled: enabled}
	k.ident.Store(&clientKeyIdent{id: clientKeyID(key), key: key})
	return k
}

// clientKeyID derives a stable identifier from the key value.
func clientKeyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "k" + hex.EncodeToString(sum[:8])
}

// keyHash indexes the pool by SHA-256 so Lookup never compares raw secrets
// with == (which would leak length and content through timing).
func keyHash(key string) [32]byte {
	return sha256.Sum256([]byte(key))
}

func (k *ClientKey) ID() string {
	return k.ident.Load().id
}

func (k *ClientKey) Name() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.name
}

func (k *ClientKey) Key() string {
	return k.ident.Load().key
}

func (k *ClientKey) IsEnabled() bool {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.enabled
}

func (k *ClientKey) setEnabled(enabled bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.enabled = enabled
}

func (k *ClientKey) setName(name string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.name = name
}

// MaskedKey hides most of the key: prefix plus the last four characters.
func (k *ClientKey) MaskedKey() string {
	return maskedKey(k.ident.Load().key)
}

func (k *ClientKey) RecordUsed() {
	now := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()
	k.lastUsedAt = now
}

func (k *ClientKey) View() ClientKeyView {
	ident := k.ident.Load()
	k.mu.RLock()
	defer k.mu.RUnlock()
	view := ClientKeyView{ID: ident.id, Name: k.name, KeyMasked: maskedKey(ident.key), Enabled: k.enabled}
	if !k.lastUsedAt.IsZero() {
		lastUsedAt := k.lastUsedAt
		view.LastUsedAt = &lastUsedAt
	}
	return view
}

type ClientKeyView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyMasked  string     `json:"key_masked"`
	Enabled    bool       `json:"enabled"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// ClientKeyPool holds the local client keys accepted by authMiddleware. The
// index maps SHA-256(key) to the key so lookup is O(1) instead of a linear
// scan, and raw secrets are never compared directly.
type ClientKeyPool struct {
	mu    sync.RWMutex
	keys  []*ClientKey
	index map[[32]byte]*ClientKey
}

func NewClientKeyPool(list []ClientKeyConfig) *ClientKeyPool {
	pool := &ClientKeyPool{index: make(map[[32]byte]*ClientKey, len(list))}
	for _, kc := range list {
		if strings.TrimSpace(kc.Key) == "" {
			continue
		}
		k := newClientKey(kc.Name, kc.Key, kc.IsEnabled())
		pool.keys = append(pool.keys, k)
		pool.index[keyHash(kc.Key)] = k
	}
	return pool
}

// Lookup finds a key by its raw bearer value, or nil. It does not filter on
// Enabled; callers decide what a disabled key means.
func (p *ClientKeyPool) Lookup(key string) *ClientKey {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.index[keyHash(key)]
}

func (p *ClientKeyPool) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.keys)
}

func (p *ClientKeyPool) EnabledCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := 0
	for _, k := range p.keys {
		if k.IsEnabled() {
			n++
		}
	}
	return n
}

func (p *ClientKeyPool) Get(id string) *ClientKey {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, k := range p.keys {
		if k.ID() == id {
			return k
		}
	}
	return nil
}

var errDuplicateClientKey = errors.New("a key with this value already exists")

// Add registers a key. An empty key value generates a fresh ccgw- key.
func (p *ClientKeyPool) Add(name, key string, enabled bool) (*ClientKey, error) {
	if strings.TrimSpace(key) == "" {
		generated, err := genAPIKey()
		if err != nil {
			return nil, err
		}
		key = generated
	}
	key = strings.TrimSpace(key)
	p.mu.Lock()
	defer p.mu.Unlock()
	hash := keyHash(key)
	if _, exists := p.index[hash]; exists {
		return nil, errDuplicateClientKey
	}
	k := newClientKey(name, key, enabled)
	p.keys = append(p.keys, k)
	p.index[hash] = k
	return k, nil
}

func (p *ClientKeyPool) Remove(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, k := range p.keys {
		if k.ID() == id {
			p.keys = append(p.keys[:i], p.keys[i+1:]...)
			delete(p.index, keyHash(k.Key()))
			return true
		}
	}
	return false
}

// SetEnabled and Rename hold the pool write lock, keeping the pool.mu ->
// key.mu order consistent with Remove and Add.
func (p *ClientKeyPool) SetEnabled(id string, enabled bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range p.keys {
		if k.ID() == id {
			k.setEnabled(enabled)
			return true
		}
	}
	return false
}

func (p *ClientKeyPool) Rename(id, name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range p.keys {
		if k.ID() == id {
			k.setName(name)
			return true
		}
	}
	return false
}

func (p *ClientKeyPool) Views() []ClientKeyView {
	p.mu.RLock()
	defer p.mu.RUnlock()
	views := make([]ClientKeyView, 0, len(p.keys))
	for _, k := range p.keys {
		views = append(views, k.View())
	}
	return views
}

func (p *ClientKeyPool) Stats() (total, enabled int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	total = len(p.keys)
	for _, k := range p.keys {
		if k.IsEnabled() {
			enabled++
		}
	}
	return total, enabled
}

func (p *ClientKeyPool) Config() []ClientKeyConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	list := make([]ClientKeyConfig, 0, len(p.keys))
	for _, k := range p.keys {
		enabled := k.IsEnabled()
		list = append(list, ClientKeyConfig{Name: k.Name(), Key: k.Key(), Enabled: &enabled})
	}
	return list
}

// SyncToConfig copies the pool into cfg ahead of a saveConfig.
func (p *ClientKeyPool) SyncToConfig(cfg *Config) {
	cfg.SetClientKeysSnapshot(p.Config())
}

// persistKeys snapshots the pool into cfg and writes config.yaml.
func persistKeys(keys *ClientKeyPool, cfg *Config) error {
	keys.SyncToConfig(cfg)
	return saveConfig(configFile, cfg)
}
