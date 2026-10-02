package app

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
)

// Version is the program version, formatted as vX.Y.Z. It is a variable so a
// release build can stamp it: -ldflags "-X cmdcode2api/internal/app.Version=v1.2.3".
var Version = "v0.3.0"

// configFile is a var so tests can redirect config persistence to a temp dir.
var configFile = "config.yaml"

func Run() {
	oauthMode := flag.Bool("oauth", false, "通过浏览器 OAuth 获取 Command Code API Key")
	oauthCallback := flag.String("oauth-callback", "", "OAuth callback URL，例如 http://server.example.com:5959/callback")
	host := flag.String("host", "", "HTTP listen host，例如 localhost 或 0.0.0.0")
	port := flag.Int("port", 0, "HTTP listen port")
	debug := flag.Bool("debug", false, "print request body and all CC SSE events to stderr")
	version := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *version {
		fmt.Printf("cmdcode2api %s (go %s)\n", Version, runtime.Version())
		os.Exit(0)
	}

	cfgPath := findConfig()

	// --oauth 模式：浏览器登录获取 API Key
	if *oauthMode {
		cfg, err := loadConfig(cfgPath)
		if err != nil {
			log.Fatalf("load config failed: %v", err)
		}
		if cfg == nil {
			// 没有配置，先生成一份
			cfg2, err := defaultConfig()
			if err != nil {
				log.Fatalf("create config failed: %v", err)
			}
			if err := writeConfigTemplate(cfgPath, cfg2); err != nil {
				log.Fatalf("create config failed: %v", err)
			}
			cfg = cfg2
		}

		cb, err := runOAuth(OAuthOptions{CallbackURL: *oauthCallback})
		if err != nil {
			log.Fatalf("OAuth failed: %v", err)
		}

		// OAuth 追加账号而不是覆盖：重复执行即可接入多个账号。
		pool := NewAccountPool(cfg.CommandCode.Accounts)
		if acct := pool.Get(accountID(cb.APIKey)); acct != nil {
			fmt.Printf("\nℹ️  API key already configured as account %q in %s\n", acct.Name(), cfgPath)
			return
		}
		name := cb.displayName()
		if name == "" {
			name = oauthAccountName(pool)
		}
		if _, err := pool.Add(name, cb.APIKey, true); err != nil {
			log.Fatalf("add oauth account failed: %v", err)
		}
		pool.SyncToConfig(cfg)
		if err := saveConfig(cfgPath, cfg); err != nil {
			log.Fatalf("save config failed: %v", err)
		}

		fmt.Printf("\n✅ API key added as account %q in %s (%d account(s) total)\n", name, cfgPath, pool.Len())
		fmt.Println("   You can now run cmdcode2api to start the server.")
		return
	}

	// 正常模式
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		log.Fatalf("load config failed: %v", err)
	}

	// 首次运行 — 生成配置
	if cfg == nil {
		cfg2, err := defaultConfig()
		if err != nil {
			log.Fatalf("create config failed: %v", err)
		}
		if err := writeConfigTemplate(cfgPath, cfg2); err != nil {
			log.Fatalf("create config failed: %v", err)
		}
		fmt.Printf(`cmdcode2api initialized.

Created config: %s
Local client key: %s
WebUI admin password: %s

Next:
  1. Run ./cmdcode2api --oauth to connect Command Code.
  2. Run ./cmdcode2api again to start the local OpenAI-compatible API.

Alternatively, just start the server — it comes up without an account, and
you can add one in the WebUI at /webui with the admin password above.

Use the local client key above as the Bearer token for your OpenAI client.
`, cfgPath, cfg2.APIKeys[0].Key, cfg2.AdminPassword)
		os.Exit(0)
	}

	// 没有上游账号也照常启动：WebUI/客户端密钥/设置均可用，
	// chat 请求会返回 503 no_accounts，直到在 WebUI 添加账号。
	if len(cfg.CommandCode.Accounts) == 0 {
		log.Printf("[WARN] no Command Code accounts configured; chat requests will return 503 until an account is added via the WebUI (/webui) or --oauth")
	}

	// WebUI 管理密码为空时生成一个，只打印一次
	adminPasswordGenerated := false
	if cfg.adminPassword() == "" {
		password, err := genAdminPassword()
		if err != nil {
			log.Fatalf("generate admin password failed: %v", err)
		}
		cfg.setAdminPassword(password)
		if err := saveConfig(cfgPath, cfg); err != nil {
			log.Fatalf("save config failed: %v", err)
		}
		adminPasswordGenerated = true
	}

	host0, port0 := cfg.Listen()
	if port0 == 0 {
		port0 = 11434
	}
	if host0 == "" {
		host0 = "localhost"
	}
	if *host != "" {
		host0 = *host
	}
	if *port != 0 {
		port0 = *port
	}
	cfg.SetListen(host0, port0)
	if cfg.UpstreamBaseURL() == "" {
		cfg.SetUpstreamBaseURL("https://api.commandcode.ai")
	}
	if *debug {
		cfg.Debug = true
		debugMode.Store(true)
	}

	// 日志同时写入环形缓冲，供 WebUI 查看
	ring := newLogRing()
	log.SetOutput(io.MultiWriter(os.Stderr, ring))

	pool := NewAccountPool(cfg.CommandCode.Accounts)
	cc := NewCCClientWithPool(pool, cfg.UpstreamBaseURL())
	usage := loadUsage()

	// 模型目录不再在启动路径上同步拉取：runServer 会起后台刷新 goroutine，
	// 上游慢或不可达时也不会推迟监听端口。
	if pool.Primary() == nil {
		log.Printf("[WARN] no enabled Command Code accounts; starting with an empty model catalog")
	}

	log.Printf("accounts: %d configured, %d enabled", pool.Len(), pool.EnabledCount())
	if adminPasswordGenerated {
		fmt.Printf("WebUI admin password generated: %s\n", cfg.adminPassword())
	}

	if err := runServer(cc, cfg, usage, ring); err != nil {
		log.Fatalf("server failed: %v", err)
	}
	if err := usage.Flush(); err != nil {
		log.Printf("save usage failed: %v", err)
	}
}

// oauthAccountName derives a label for an OAuth-added account.
func oauthAccountName(pool *AccountPool) string {
	return fmt.Sprintf("oauth-%d", pool.Len()+1)
}

func findConfig() string {
	return configFile
}
