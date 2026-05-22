package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/campbellcharlie/lorg/apps/app"
	"github.com/campbellcharlie/lorg/internal/config"
	_ "github.com/campbellcharlie/lorg/internal/logflags"
	"github.com/campbellcharlie/lorg/internal/utils"
)

var conf config.Config
var API app.Backend

var HostAddress string
var ProjectPath string
var ProxyAddress string // removed, we use api now
var ProjectsDir string  // directory containing per-project .db files for the UI switcher
var showLogs bool
var allowLAN bool       // when true, permit non-loopback callers to /api/*

func init() {
	// Ensure timestamps are included in standard log output.
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}

func initialize() {

	if !showLogs {
		// log.SetOutput(io.Discard)
	}

	var err error
	conf.HostAddr, err = utils.CheckAndFindAvailablePort(HostAddress)
	if err != nil {
		log.Fatalln(err)
	} else {
		if conf.HostAddr != HostAddress {
			fmt.Println("\nInfo: Host address is already in use. Using ", conf.HostAddr)
		}
	}

	// Optional override: if LORG_TEMPLATE_DIR isn't set, keep the default config value.
	if templateDir := strings.TrimSpace(os.Getenv("LORG_TEMPLATE_DIR")); templateDir != "" {
		conf.TemplateDirectory = templateDir
	}

	conf.Initiate()
}

func main() {
	flag.StringVar(&HostAddress, "host", "127.0.0.1:8090", "Host address to listen on")
	flag.StringVar(&ProxyAddress, "proxy", "127.0.0.1:8888", "Proxy address to listen on")
	flag.StringVar(&ProjectPath, "path", "", "Project directory path (lorgdb storage)")
	flag.StringVar(&ProjectsDir, "projects-dir", "", "Directory containing per-project .db files for the UI switcher (default: $HOME/.lorg/projects)")
	flag.BoolVar(&showLogs, "log", false, "Show debug logs")
	flag.StringVar(&conf.MCPToken, "mcp-token", "", "Bearer token for MCP endpoint authentication")
	flag.BoolVar(&allowLAN, "allow-lan", false, "Allow API access from non-loopback addresses (off by default; only enable on a trusted network)")

	flag.Parse()

	// Allow MCP_TOKEN env to populate the token if -mcp-token wasn't passed.
	// The -allow-lan refusal below references this fallback.
	if conf.MCPToken == "" {
		if env := strings.TrimSpace(os.Getenv("MCP_TOKEN")); env != "" {
			conf.MCPToken = env
		}
	}

	// Refuse to start when -allow-lan is set without a token: the combination
	// would silently expose unauthenticated MCP to the LAN. Explicit token
	// required — no auto-generation, so the failure is obvious.
	if allowLAN && conf.MCPToken == "" {
		fmt.Fprintln(os.Stderr, "lorg: -allow-lan requires -mcp-token (or MCP_TOKEN env) to avoid exposing unauthenticated MCP to the LAN")
		os.Exit(1)
	}

	app.SetTrustNetwork(allowLAN)

	if len(os.Args) > 1 {
		initialize()

		// Resolve projects-dir. Default: $HOME/.lorg/projects.
		if strings.TrimSpace(ProjectsDir) == "" {
			if home, err := os.UserHomeDir(); err == nil {
				ProjectsDir = home + "/.lorg/projects"
			}
		}
		if ProjectsDir != "" {
			_ = os.MkdirAll(ProjectsDir, 0755)
			conf.ProjectsDBDirectory = ProjectsDir
		}

		fmt.Println("Initializing done")
		fmt.Println("Projects DB directory:", ProjectsDir)
		serve(ProjectPath)
	} else {
		fmt.Println("No project path provided")
	}
}
