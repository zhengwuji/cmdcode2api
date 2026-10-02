package app

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// AccountConfig is one upstream Command Code credential in config.yaml.
type AccountConfig struct {
	Name    string `yaml:"name"`
	APIKey  string `yaml:"api_key"`
	Enabled *bool  `yaml:"enabled,omitempty"` // nil means enabled
}

func (a AccountConfig) IsEnabled() bool {
	return a.Enabled == nil || *a.Enabled
}

// ClientKeyConfig is one local bearer key that clients use to call this
// gateway.
type ClientKeyConfig struct {
	Name    string `yaml:"name"`
	Key     string `yaml:"key"`
	Enabled *bool  `yaml:"enabled,omitempty"` // nil means enabled
}

func (k ClientKeyConfig) IsEnabled() bool {
	return k.Enabled == nil || *k.Enabled
}

type Config struct {
	APIKey string `yaml:"api_key,omitempty"`
	// APIKeys is the list of local client keys. The legacy single api_key
	// field above is migrated into it on load and cleared on save once the
	// list is non-empty.
	APIKeys []ClientKeyConfig `yaml:"api_keys,omitempty"`
	// AdminPassword guards the WebUI admin API. Generated on first start when
	// empty; changeable at runtime via the admin API.
	AdminPassword string `yaml:"admin_password,omitempty"`
	// WebUI controls whether the binary serves the embedded admin interface.
	// nil means enabled.
	WebUI *bool  `yaml:"webui,omitempty"`
	Host  string `yaml:"host"`
	Port  int    `yaml:"port"`

	CommandCode struct {
		// APIKey is the legacy single-account field. It is migrated into
		// Accounts on load and cleared on save once accounts exist.
		APIKey   string          `yaml:"api_key,omitempty"`
		BaseURL  string          `yaml:"base_url,omitempty"`
		Accounts []AccountConfig `yaml:"accounts,omitempty"`
	} `yaml:"commandcode"`

	ExcludeModels []string `yaml:"exclude_models"`
	Debug         bool     `yaml:"-"` // runtime flag, not persisted

	// mu guards every field that the admin API mutates while request handlers
	// and background jobs read them: ExcludeModels, CommandCode.*, APIKeys,
	// AdminPassword, Host, Port and WebUI.
	mu sync.RWMutex
}

// Listen returns the configured bind host and port.
func (c *Config) Listen() (string, int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Host, c.Port
}

func (c *Config) SetListen(host string, port int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Host = host
	c.Port = port
}

func (c *Config) WebUIEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.webUIEnabledLocked()
}

func (c *Config) SetWebUI(enabled *bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.WebUI = enabled
}

func (c *Config) webUIEnabledLocked() bool {
	return c.WebUI == nil || *c.WebUI
}

func (c *Config) Excludes() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.ExcludeModels...)
}

func (c *Config) SetExcludes(list []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ExcludeModels = list
}

func (c *Config) UpstreamBaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.CommandCode.BaseURL
}

func (c *Config) SetUpstreamBaseURL(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.CommandCode.BaseURL = url
}

// SetAccountsSnapshot replaces the persisted upstream account list together
// with the legacy single-key field, under the same lock saveConfig uses.
func (c *Config) SetAccountsSnapshot(accounts []AccountConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.CommandCode.Accounts = accounts
	if len(accounts) > 0 {
		// Keeping the legacy field would resurrect deleted accounts on load.
		c.CommandCode.APIKey = ""
	}
}

// SetClientKeysSnapshot replaces the persisted client key list together with
// the legacy single-key field, under the same lock saveConfig uses.
func (c *Config) SetClientKeysSnapshot(keys []ClientKeyConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.APIKeys = keys
	if len(keys) > 0 {
		c.APIKey = ""
	}
}

// AdminPasswordHash is used only by tests that need to compare credentials
// without reaching into the lock.
func (c *Config) adminPassword() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.AdminPassword
}

func (c *Config) setAdminPassword(password string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.AdminPassword = password
}

func defaultConfig() (*Config, error) {
	apiKey, err := genAPIKey()
	if err != nil {
		return nil, err
	}
	adminPassword, err := genAdminPassword()
	if err != nil {
		return nil, err
	}
	// 生成的客户端密钥只写入 api_keys 列表；旧字段 api_key 仅用于兼容
	// 已有的手工配置，不再出现在新生成的文件里。
	c := &Config{
		AdminPassword: adminPassword,
		Host:          "localhost",
		Port:          11434,
		ExcludeModels: []string{"gpt-", "claude-", "gemini-"},
	}
	c.APIKeys = []ClientKeyConfig{{Name: "default", Key: apiKey}}
	c.CommandCode.BaseURL = "https://api.commandcode.ai"
	return c, nil
}

func genAPIKey() (string, error) {
	key, err := randomHex(24)
	if err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return "ccgw-" + key, nil
}

func genAdminPassword() (string, error) {
	// 12 位纯随机字符即可，不加可读前缀
	return randomPassword(12)
}

// loadConfig reads config.yaml and migrates the legacy single-key fields into
// the accounts and client-keys lists.
func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(cfg.CommandCode.Accounts) == 0 && cfg.CommandCode.APIKey != "" {
		cfg.CommandCode.Accounts = []AccountConfig{{Name: "default", APIKey: cfg.CommandCode.APIKey}}
	}
	if len(cfg.APIKeys) == 0 && cfg.APIKey != "" {
		cfg.APIKeys = []ClientKeyConfig{{Name: "default", Key: cfg.APIKey}}
	}
	return &cfg, nil
}

func saveConfig(path string, cfg *Config) error {
	cfg.mu.Lock()
	// Non-empty lists are the source of truth; keeping the legacy fields
	// would resurrect deleted keys on the next load.
	if len(cfg.CommandCode.Accounts) > 0 {
		cfg.CommandCode.APIKey = ""
	}
	if len(cfg.APIKeys) > 0 {
		cfg.APIKey = ""
	}
	data, err := yaml.Marshal(cfg)
	cfg.mu.Unlock()
	if err != nil {
		return err
	}
	// Write via a temp file + rename so a crash mid-write cannot leave a
	// truncated config.yaml behind.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func writeConfigTemplate(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	template := "# cmdcode2api configuration\n" +
		"# See README.md for all options.\n" +
		"\n" +
		"# exclude_models is enabled by default for premium/non-open-source models\n" +
		"# (e.g., GPT, Claude, Gemini) that may be unavailable on certain plans.\n" +
		"# Remove entries below or set exclude_models: [] to make all models available.\n" +
		"\n" +
		"# commandcode.accounts holds one or more upstream API keys; requests are\n" +
		"# rotated across them. api_keys holds the local bearer keys that clients\n" +
		"# use to call this gateway; both are editable in the WebUI at /webui.\n" +
		"\n" +
		string(data)
	return os.WriteFile(path, []byte(template), 0600)
}
