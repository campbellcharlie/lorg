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
var showLogs bool

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
	flag.StringVar(&ProjectPath, "path", "", "Project directory path")
	flag.BoolVar(&showLogs, "log", false, "Show debug logs")
	flag.StringVar(&conf.MCPToken, "mcp-token", "", "Bearer token for MCP endpoint authentication")
	flag.BoolVar(&conf.EnableTerminal, "enable-terminal", false, "Enable xterm terminal routes (disabled by default)")

	flag.Parse()

	if len(os.Args) > 1 {
		initialize()

		fmt.Println("Initializing done")
		serve(ProjectPath)
	} else {
		fmt.Println("No project path provided")
	}
}
