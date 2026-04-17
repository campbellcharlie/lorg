package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"runtime"

	"github.com/campbellcharlie/lorg/apps/launcher"
	"github.com/campbellcharlie/lorg/internal/config"
	"github.com/campbellcharlie/lorg/internal/lorgdb"
	_ "github.com/campbellcharlie/lorg/internal/logflags"
	"github.com/campbellcharlie/lorg/internal/process"
	"github.com/campbellcharlie/lorg/internal/updater"
	"github.com/campbellcharlie/lorg/internal/utils"
	"github.com/campbellcharlie/lorg/lrx/rawproxy"
	"github.com/campbellcharlie/lorg/lrx/version"
	"github.com/spf13/cobra"
)

var noProxy bool
var MainHostAddress string = "127.0.0.1:8090"
var hostFixed bool // if true, skip CheckAndFindAvailablePort
var MainProxyAddress string = "127.0.0.1:8888"
var showLogs = false

func init() {
	// Ensure timestamps are included in standard log output.
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}

var launch *launcher.Launcher
var conf config.Config

func setConfig() {

	conf.Initiate()

	caCrtPath := filepath.Join(conf.ConfigDirectory, "ca.crt")
	caKeyPath := filepath.Join(conf.ConfigDirectory, "ca.key")

	// If certificates don't exist, generate them using rawproxy
	if !fileExists(caCrtPath) || !fileExists(caKeyPath) {
		_, certPath, _, err := rawproxy.GenerateMITMCA(conf.ConfigDirectory)
		if err != nil {
			log.Printf("[Warning] Failed to generate CA certificate: %v", err)
		} else {
			log.Printf("[Certificate] CA certificate generated at: %s", certPath)
		}
	} else {
		log.Printf("[Certificate] CA certificate already exists at: %s", caCrtPath)
	}
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func initialize() {

	setConfig()

	if !showLogs {
		log.SetOutput(io.Discard)
	}

	startCore()

}

func completionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "completion",
		Short: "This meant to be hidden",
	}
}

var rootCmd = &cobra.Command{
	Use:   "lorg",
	Short: "Center of your web hacking operations",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		printBanner()
	},
	// When running just `lorg`, show the command structure (help)
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() {
	completion := completionCommand()

	// mark completion hidden
	completion.Hidden = true
	rootCmd.AddCommand(completion)
}

func main() {

	rootCmd.AddCommand(&cobra.Command{
		Use:   "config",
		Short: "Set config directory",
		Run: func(cmd *cobra.Command, args []string) {
			setConfig()
		},
	})

	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start lorg",
		Run: func(cmd *cobra.Command, args []string) {
			if h, _ := cmd.Flags().GetString("host"); h != "" {
				MainHostAddress = h
				hostFixed = true
			}
			initialize()
		},
	}
	startCmd.Flags().String("host", "", "host address to listen on (default 127.0.0.1:8090)")
	rootCmd.AddCommand(startCmd)

	rootCmd.AddCommand(&cobra.Command{
		Use:   "update [binary]",
		Short: "Update lorg binaries to the latest release",
		Long: `Update lorg binaries from GitHub Releases.

Without arguments, updates all binaries (lorg, lorg, lorg-tool).
Specify a binary name to update only that one.`,
		Args: cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			allBinaries := []string{"lorg", "lorg", "lorg-tool"}
			targets := allBinaries
			if len(args) == 1 {
				targets = []string{args[0]}
			}

			token := updater.GetToken()
			if token == "" {
				fmt.Println("Warning: No GitHub token found. Set GITHUB_TOKEN or GH_TOKEN for private repo access.")
			}

			fmt.Println("Checking for updates...")
			release, err := updater.CheckLatestVersion(token)
			if err != nil {
				fmt.Printf("Error checking for updates: %v\n", err)
				os.Exit(1)
			}

			current := version.CURRENT_BACKEND_VERSION
			latest := release.TagName
			fmt.Printf("Current version: v%s\n", current)
			fmt.Printf("Latest version:  %s\n", latest)
			fmt.Printf("Platform:        %s/%s\n", runtime.GOOS, runtime.GOARCH)

			if !updater.NeedsUpdate(current, latest) {
				fmt.Println("\nAlready up to date!")
				return
			}

			fmt.Printf("\nUpdating to %s...\n\n", latest)

			for _, name := range targets {
				// Clean up .old files from previous Windows updates
				if binPath, err := updater.FindBinaryPath(name); err == nil {
					updater.CleanupOldBinaries(binPath)
				}

				asset, err := updater.FindAsset(release, name)
				if err != nil {
					fmt.Printf("  [SKIP] %s: %v\n", name, err)
					continue
				}

				binPath, err := updater.FindBinaryPath(name)
				if err != nil {
					fmt.Printf("  [SKIP] %s: %v\n", name, err)
					continue
				}

				fmt.Printf("  Updating %s (%s)...", name, binPath)
				if err := updater.UpdateBinary(asset.URL, binPath, token); err != nil {
					fmt.Printf(" FAILED: %v\n", err)
					continue
				}
				fmt.Println(" OK")
			}

			fmt.Printf("\nUpdated to %s. Restart lorg to use the new version.\n", latest)
		},
	})

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func printBanner() {
	fmt.Printf(`
G R R R . . . O X Y           v%s
`, version.CURRENT_BACKEND_VERSION)
}

func startCore() {
	// Open the launcher's own database
	dbPath := filepath.Join(conf.ProjectsDirectory, "lorg", "pb_data", "data.db")
	db, err := lorgdb.Open(dbPath)
	if err != nil {
		log.Fatalf("[Launcher] Failed to open database: %v", err)
	}

	if err := db.RunMigrations(); err != nil {
		log.Fatalf("[Launcher] Failed to run migrations: %v", err)
	}

	host := MainHostAddress
	if !hostFixed {
		var err error
		host, err = utils.CheckAndFindAvailablePort(MainHostAddress)
		if err != nil {
			panic(err)
		}
	}

	conf.HostAddr = host

	launch = &launcher.Launcher{
		DB:         db,
		Config:     &conf,
		CmdChannel: make(chan process.RunCommandData),
	}

	// Reset project and tool states on startup
	launch.ResetProjectStates()
	launch.ResetToolsStates()

	// Signal that initialization is complete before starting the server
	fmt.Println("Starting main app at: ", host)

	// Serve blocks until the server is stopped
	launch.Serve()

	// If we reach here, check for ErrServerClosed
	_ = errors.Is(err, http.ErrServerClosed)
}
