// lorg-app — desktop wrapper around the lorg HTTP UI.
//
// Spawns the lorg backend in-process on a free local port, then opens a
// native WebKit (macOS) / WebView2 (Windows) / WebKit2GTK (Linux) window
// that reverse-proxies to it through Wails's asset server.
//
// Browser users still run `lorg-bin` exactly as before — this is purely
// additive packaging. Same DB, same MCP, same UI.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// backendURL is the local URL the embedded lorg backend binds to. We
// proxy every WebKit request to it, so this is the *real* origin that
// serves the UI; the window just shows it through Wails's URL scheme.
var backendURL atomic.Value // *url.URL

// backendCmd holds the lorg-bin child process so we can stop it on exit.
var backendCmd atomic.Value // *exec.Cmd

func main() {
	port := resolveBackendPort()
	if err := startLorgBackend(port); err != nil {
		log.Fatalf("failed to start lorg backend: %v", err)
	}
	if err := waitForBackend(port, 15*time.Second); err != nil {
		log.Fatalf("backend never came up on :%d — %v", port, err)
	}

	bu, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	backendURL.Store(bu)
	log.Printf("[lorg-app] backend at %s", bu)

	app := &lorgApp{}
	err := wails.Run(&options.App{
		Title:     "lorg",
		Width:     1400,
		Height:    900,
		MinWidth:  900,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Handler: newProxyHandler(),
		},
		Menu:             buildAppMenu(app),
		BackgroundColour: &options.RGBA{R: 24, G: 24, B: 28, A: 1},
		OnStartup:        app.OnStartup,
		OnDomReady:       app.OnDomReady,
		OnShutdown:       app.OnShutdown,
		Bind:             []interface{}{app},
		Mac: &mac.Options{
			// Standard macOS title bar — full chrome (traffic lights +
			// title), entire strip is draggable. Tried HiddenInset with
			// CSS-injected drag region but the user preference was
			// to keep the conventional macOS look.
			WebviewIsTransparent: false,
			About: &mac.AboutInfo{
				Title:   "lorg",
				Message: "AI-powered penetration testing proxy.\nDesktop wrapper around the lorg HTTP UI.",
			},
		},
	})
	if err != nil {
		log.Println("wails error:", err)
	}
}

// ---------------------------------------------------------------------------
// app lifecycle
// ---------------------------------------------------------------------------

type lorgApp struct {
	ctx context.Context
}

func (a *lorgApp) OnStartup(ctx context.Context) {
	a.ctx = ctx
}

// OnDomReady navigates the WebKit window away from the Wails internal
// asset server and onto the actual lorg backend origin
// (http://127.0.0.1:<port>). Routing every request through Wails's
// reverse proxy turned out to drop or transform some responses in
// ways that broke the frontend's project / proxy lists. By
// navigating directly we make the WebKit behave like Safari hitting
// the same URL — no proxy in between, exact same JS environment as
// the browser path.
func (a *lorgApp) OnDomReady(ctx context.Context) {
	bu := backendURL.Load().(*url.URL)
	if bu == nil {
		return
	}
	// Skip if we've already navigated (OnDomReady fires on every
	// load, including post-redirect).
	js := fmt.Sprintf(
		`if (location.href.indexOf(%q) !== 0) location.replace(%q + '/');`,
		bu.String(), bu.String(),
	)
	wailsruntime.WindowExecJS(ctx, js)
}

// OnShutdown fires when the user closes the window. We tear down the
// embedded backend so its proxy/MCP listeners release their ports.
func (a *lorgApp) OnShutdown(ctx context.Context) {
	stopLorgBackend()
}

// OpenInBrowser is exposed to the frontend so the user can pop the UI
// out into their normal browser if they want to keep using the desktop
// shell for some sessions and a tab for others. Same backend either way.
func (a *lorgApp) OpenInBrowser() {
	bu := backendURL.Load().(*url.URL)
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", bu.String()).Start()
	case "windows":
		_ = exec.Command("cmd", "/c", "start", bu.String()).Start()
	default:
		_ = exec.Command("xdg-open", bu.String()).Start()
	}
}

// ---------------------------------------------------------------------------
// reverse proxy: WebKit window → in-process lorg backend
// ---------------------------------------------------------------------------

func newProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bu := backendURL.Load().(*url.URL)
		if bu == nil {
			http.Error(w, "lorg backend not yet ready", http.StatusServiceUnavailable)
			return
		}
		proxy := httputil.NewSingleHostReverseProxy(bu)
		// WKWebView aggressively caches API JSON responses if the backend
		// doesn't set Cache-Control. That bit users on first launch:
		// if the UI fetched /api/proxy/list before the backend's auto-
		// start goroutine fired, WebKit cached the empty result and
		// served it on every reload until quit. Stamp no-store on every
		// /api/* response — these are dynamic anyway, no value in caching.
		proxy.ModifyResponse = func(resp *http.Response) error {
			if strings.HasPrefix(resp.Request.URL.Path, "/api/") {
				resp.Header.Set("Cache-Control", "no-store")
			}
			return nil
		}
		proxy.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// backend process management
// ---------------------------------------------------------------------------

// startLorgBackend launches the regular `lorg-bin` binary as a child
// process bound to the chosen port. We deliberately reuse the existing
// binary instead of importing apps/app directly so the desktop shell
// can ship without being recompiled every time the backend changes
// (and so a misbehaving backend can be restarted without the user
// losing their window).
func startLorgBackend(port int) error {
	binPath, err := lorgBinPath()
	if err != nil {
		return err
	}

	args := []string{
		"-host", fmt.Sprintf("127.0.0.1:%d", port),
		"-path", defaultProjectDir(),
	}
	cmd := exec.Command(binPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("exec lorg-bin: %w", err)
	}
	backendCmd.Store(cmd)
	log.Printf("[lorg-app] spawned lorg-bin (pid=%d) on :%d", cmd.Process.Pid, port)
	return nil
}

func stopLorgBackend() {
	v := backendCmd.Load()
	if v == nil {
		return
	}
	cmd, ok := v.(*exec.Cmd)
	if !ok || cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
	}
}

func lorgBinPath() (string, error) {
	// 1. Sibling of the app binary (production install layout).
	if exe, err := os.Executable(); err == nil {
		side := filepath.Join(filepath.Dir(exe), "lorg-bin")
		if runtime.GOOS == "windows" {
			side += ".exe"
		}
		if _, err := os.Stat(side); err == nil {
			return side, nil
		}
	}
	// 2. Repo root during development.
	if cwd, err := os.Getwd(); err == nil {
		repoBin := filepath.Join(cwd, "lorg-bin")
		if _, err := os.Stat(repoBin); err == nil {
			return repoBin, nil
		}
	}
	// 3. PATH lookup as a last resort.
	if p, err := exec.LookPath("lorg-bin"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("lorg-bin not found next to lorg-app, in cwd, or on PATH")
}

func defaultProjectDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".lorg/default"
	}
	return filepath.Join(home, ".lorg", "default")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// pickFreePort lets the OS pick an available TCP port for the backend.
func pickFreePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("could not pick a free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// resolveBackendPort decides which local port the embedded lorg-bin
// should bind to. Resolution order:
//
//   1. LORG_APP_PORT env var (explicit override)
//   2. Default :8090 (matches the standalone lorg-bin default — keeps
//      Claude Code's MCP config "just working" without per-launch
//      reconfiguration)
//   3. Free port from the OS, but only if the preferred port is in
//      use AND not already serving a lorg-bin (in which case we
//      abort — that means a user copy is running and lorg-app would
//      fight it for the SQLite write lock anyway).
//
// The fallback is a soft warning rather than a hard error so a user
// who has port :8090 occupied by something else (Vite, FastAPI, etc.)
// still gets a working desktop window — they just have to point any
// MCP integrations at the new port shown in the log.
func resolveBackendPort() int {
	preferred := 8090
	if env := os.Getenv("LORG_APP_PORT"); env != "" {
		if p, err := strconv.Atoi(env); err == nil && p > 0 && p < 65536 {
			preferred = p
		} else {
			log.Printf("[lorg-app] ignoring invalid LORG_APP_PORT=%q", env)
		}
	}
	if portFree(preferred) {
		return preferred
	}
	if probeLorgBin(preferred) {
		log.Fatalf("[lorg-app] :%d is already serving a lorg-bin — quit it first (lorg-app and lorg-bin can't share the SQLite write lock)", preferred)
	}
	free := pickFreePort()
	log.Printf("[lorg-app] :%d is in use by something else; falling back to :%d", preferred, free)
	return free
}

// portFree returns true when nothing is listening on 127.0.0.1:port.
func portFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// probeLorgBin returns true if the process listening on :port responds
// like a lorg backend (i.e. /mcp/health returns 200). Used to give a
// loud, specific error when the user has a copy of lorg-bin running.
func probeLorgBin(port int) bool {
	c := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/mcp/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

func waitForBackend(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 1 * time.Second}

	// Stage 1: /mcp/health responds — Echo router is up, but lorg-bin
	// auto-starts the MITM proxy on a 1s-delayed goroutine, so the proxy
	// list is still empty here.
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/mcp/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				goto stage2
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for /mcp/health after %s", timeout)

stage2:
	// Stage 2: /api/proxy/list returns at least one proxy. Without this
	// the WebKit window opens before the auto-start goroutine fires,
	// the UI's initial render shows "No proxy running" + "No .db files"
	// and never auto-refreshes — looks broken until the user hits ⇧⌘R.
	// We give the auto-start up to 5s after /mcp/health which is plenty
	// (it usually fires <1s in).
	soft := time.Now().Add(5 * time.Second)
	if soft.Before(deadline) {
		deadline = soft
	}
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/api/proxy/list")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && bytes.Contains(body, []byte(`"id":`)) {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Soft-fail: window opens anyway, just with the same race the user
	// had before. Better than blocking startup if the proxy never starts
	// for some unrelated reason.
	log.Printf("[lorg-app] proxy auto-start didn't appear in time; UI may need ⇧⌘R")
	return nil
}

// ---------------------------------------------------------------------------
// menu — native menu bar so the window feels like a real app
// ---------------------------------------------------------------------------

func buildAppMenu(app *lorgApp) *menu.Menu {
	root := menu.NewMenu()

	// macOS app menu (auto-hidden on other OSes via Wails).
	if runtime.GOOS == "darwin" {
		appMenu := root.AddSubmenu("lorg")
		appMenu.AddText("About lorg", nil, nil)
		appMenu.AddSeparator()
		appMenu.AddText("Hide lorg", keys.CmdOrCtrl("h"), func(_ *menu.CallbackData) {
			wailsruntime.WindowMinimise(app.ctx)
		})
		appMenu.AddSeparator()
		appMenu.AddText("Quit lorg", keys.CmdOrCtrl("q"), func(_ *menu.CallbackData) {
			wailsruntime.Quit(app.ctx)
		})
	}

	fileMenu := root.AddSubmenu("File")
	fileMenu.AddText("Open in Browser", keys.CmdOrCtrl("b"), func(_ *menu.CallbackData) {
		app.OpenInBrowser()
	})
	fileMenu.AddSeparator()
	fileMenu.AddText("Reload", keys.CmdOrCtrl("r"), func(_ *menu.CallbackData) {
		wailsruntime.WindowReload(app.ctx)
	})

	editMenu := root.AddSubmenu("Edit")
	editMenu.AddText("Undo", keys.CmdOrCtrl("z"), nil)
	editMenu.AddText("Redo", keys.Combo("z", keys.CmdOrCtrlKey, keys.ShiftKey), nil)
	editMenu.AddSeparator()
	editMenu.AddText("Cut", keys.CmdOrCtrl("x"), nil)
	editMenu.AddText("Copy", keys.CmdOrCtrl("c"), nil)
	editMenu.AddText("Paste", keys.CmdOrCtrl("v"), nil)
	editMenu.AddText("Select All", keys.CmdOrCtrl("a"), nil)
	editMenu.AddSeparator()
	editMenu.AddText("Find", keys.CmdOrCtrl("f"), nil) // forwarded to UI's Cmd+F handler

	viewMenu := root.AddSubmenu("View")
	viewMenu.AddText("Reload", keys.CmdOrCtrl("r"), func(_ *menu.CallbackData) {
		wailsruntime.WindowReload(app.ctx)
	})
	viewMenu.AddText("Force Reload (clear cache)", keys.Combo("r", keys.CmdOrCtrlKey, keys.ShiftKey), func(_ *menu.CallbackData) {
		wailsruntime.WindowReloadApp(app.ctx)
	})

	// Standard macOS Window menu. macOS 15+ adds system-level "Move & Resize"
	// submenu with Split View tiling options automatically; hovering the
	// green title-bar button also exposes Tile Left / Tile Right. The
	// entries below are the canonical AppKit ones every native app exposes.
	winMenu := root.AddSubmenu("Window")
	winMenu.AddText("Minimize", keys.CmdOrCtrl("m"), func(_ *menu.CallbackData) {
		wailsruntime.WindowMinimise(app.ctx)
	})
	winMenu.AddText("Zoom", nil, func(_ *menu.CallbackData) {
		// Toggle macOS zoom (green button) — fills available screen space
		// without entering true fullscreen, leaving menu bar + dock visible.
		wailsruntime.WindowToggleMaximise(app.ctx)
	})
	winMenu.AddText("Toggle Fullscreen", keys.Combo("f", keys.CmdOrCtrlKey, keys.ControlKey), func(_ *menu.CallbackData) {
		if wailsruntime.WindowIsFullscreen(app.ctx) {
			wailsruntime.WindowUnfullscreen(app.ctx)
		} else {
			wailsruntime.WindowFullscreen(app.ctx)
		}
	})
	winMenu.AddSeparator()
	// Tile Left / Right via WindowSetPosition + WindowSetSize. macOS 15+
	// users can also use the system Window > Move & Resize menu, but
	// providing explicit entries here is more discoverable and works on
	// older macOS versions too.
	winMenu.AddText("Tile to Left of Screen", keys.Combo("[", keys.CmdOrCtrlKey, keys.OptionOrAltKey), func(_ *menu.CallbackData) {
		tileWindow(app.ctx, "left")
	})
	winMenu.AddText("Tile to Right of Screen", keys.Combo("]", keys.CmdOrCtrlKey, keys.OptionOrAltKey), func(_ *menu.CallbackData) {
		tileWindow(app.ctx, "right")
	})
	winMenu.AddText("Center Window", nil, func(_ *menu.CallbackData) {
		wailsruntime.WindowCenter(app.ctx)
	})

	return root
}

// tileWindow snaps the window to the left or right half of the current
// screen — the same effect as macOS's hover-on-green-button tiling, but
// usable from the menu bar (and on macOS versions older than 15 that
// don't have the system-level tiling submenu).
func tileWindow(ctx context.Context, side string) {
	screens, err := wailsruntime.ScreenGetAll(ctx)
	if err != nil || len(screens) == 0 {
		return
	}
	w, h := screens[0].Size.Width, screens[0].Size.Height
	half := w / 2
	if side == "right" {
		wailsruntime.WindowSetPosition(ctx, half, 0)
	} else {
		wailsruntime.WindowSetPosition(ctx, 0, 0)
	}
	wailsruntime.WindowSetSize(ctx, half, h)
}
