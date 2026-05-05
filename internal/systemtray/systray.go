// Package systemtray runs the OS system tray UI for a wick-powered app:
// start/stop the local HTTP server and the background job worker (both
// in-process via cancellable goroutines), and install or uninstall the
// app's MCP entry into detected MCP clients (Claude Desktop, Cursor,
// etc).
//
// Wired into downstream apps via the `tray` subcommand registered in
// app.Run(). Not a standalone main.
package systemtray

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"fyne.io/systray"

	"github.com/yogasw/wick/internal/mcpconfig"
	"github.com/yogasw/wick/internal/pkg/api"
	"github.com/yogasw/wick/internal/pkg/config"
	"github.com/yogasw/wick/internal/pkg/worker"
	"github.com/yogasw/wick/internal/userconfig"
)

var (
	mu sync.Mutex

	serverCancel context.CancelFunc
	serverDone   chan struct{}
	serverPort   int

	workerCancel context.CancelFunc
	workerDone   chan struct{}

	project     string
	appName     string
	appVersion  string
	wickVersion string
	logPath     string
	userCfg     userconfig.Config
	cfgPath     string
)

// Run starts the system tray and blocks until the user picks Quit.
// projectDir is the wick project directory (CWD where the binary lives).
// name is the MCP server name written into client configs (default: dir name).
// appVer / wickVer are the build-injected versions surfaced as a
// disabled info row in the tray menu.
func Run(projectDir, name, appVer, wickVer string) {
	project = projectDir
	if name == "" {
		name = filepath.Base(projectDir)
	}
	appName = name
	appVersion = appVer
	wickVersion = wickVer
	serverPort = config.Load().App.Port

	lock, err := acquireSingleInstance()
	if err != nil {
		log.Printf("single-instance: %v", err)
		return
	}
	defer lock.Close()

	if cfg, err := userconfig.Load(appName); err == nil {
		userCfg = cfg
	}
	if p, err := userconfig.Path(appName); err == nil {
		cfgPath = p
	}
	// Persist defaults on first launch so "Open config file" always
	// has a real file to open.
	if err := userconfig.Save(appName, userCfg); err != nil {
		log.Printf("save config (initial): %v", err)
	}

	if p, cleanup, err := setupLogFile(appName); err == nil {
		logPath = p
		defer cleanup()
	}

	systray.Run(onReady, onExit)
}

// fmtVer normalises a version string for display: prefixes a single
// "v" for semver-looking values, leaves "dev" / "unknown" / empty
// alone so the menu doesn't end up reading "vdev" or "vv0.6.1".
func fmtVer(v string) string {
	if v == "" {
		return "?"
	}
	if v == "dev" || v == "unknown" {
		return v
	}
	return "v" + strings.TrimPrefix(v, "v")
}

func saveUserCfg() {
	if err := userconfig.Save(appName, userCfg); err != nil {
		log.Printf("save config: %v", err)
	}
}

type clientUI struct {
	c   mcpconfig.Client
	sub *systray.MenuItem
}

func (u *clientUI) refresh() {
	present, installed := mcpconfig.IsInstalled(u.c, appName)
	switch {
	case !present:
		u.sub.SetTitle(u.c.Label + " — not configured yet")
	case installed:
		u.sub.SetTitle(u.c.Label + "  ✓ installed")
	default:
		u.sub.SetTitle(u.c.Label + " — not installed")
	}
}

func refreshIcon() {
	systray.SetIcon(wickIcon(isServerRunning(), isWorkerRunning()))
}

func onReady() {
	refreshIcon()
	systray.SetTitle(appName)
	systray.SetTooltip(appName + " — " + project)

	mInfo := systray.AddMenuItem(fmt.Sprintf("%s %s  (wick %s)", appName, fmtVer(appVersion), fmtVer(wickVersion)), project)
	mInfo.Disable()
	systray.AddSeparator()

	mServer := systray.AddMenuItem("Start server", "Toggle HTTP server")
	mWorker := systray.AddMenuItem("Start worker", "Toggle background job worker")
	mLogs := systray.AddMenuItem("Open logs", "Open "+logPath)
	if logPath == "" {
		mLogs.Disable()
	}
	systray.AddSeparator()

	mMCP := systray.AddMenuItem("MCP", "Install MCP entry into client config")
	mInstallAll := mMCP.AddSubMenuItem("Install all detected", "Install into every detected client")
	mUninstallAll := mMCP.AddSubMenuItem("Uninstall all", "Remove from every detected client")
	mExample := mMCP.AddSubMenuItem("Show example config", "Open generated MCP config snippet in editor")
	mMCP.AddSubMenuItem("─────────────", "").Disable()

	clients := mcpconfig.Detected(project)
	uis := make([]*clientUI, 0, len(clients))
	for i := range clients {
		c := clients[i]
		ui := &clientUI{c: c, sub: mMCP.AddSubMenuItem(c.Label, c.Path)}
		ui.refresh()
		uis = append(uis, ui)
		install := ui.sub.AddSubMenuItem("Install / update", "Write entry into "+c.Path)
		uninstall := ui.sub.AddSubMenuItem("Uninstall", "Remove entry from "+c.Path)
		open := ui.sub.AddSubMenuItem("Open config", "Open "+c.Path)
		go func(ui *clientUI, install, uninstall, open *systray.MenuItem) {
			for {
				select {
				case <-install.ClickedCh:
					if err := installOne(ui.c); err != nil {
						log.Printf("install %s: %v", ui.c.ID, err)
					} else {
						ui.refresh()
					}
				case <-uninstall.ClickedCh:
					if err := mcpconfig.Uninstall(ui.c, appName); err != nil {
						log.Printf("uninstall %s: %v", ui.c.ID, err)
					} else {
						ui.refresh()
					}
				case <-open.ClickedCh:
					if err := openInEditor(ui.c.Path); err != nil {
						log.Printf("open %s: %v", ui.c.Path, err)
					}
				}
			}
		}(ui, install, uninstall, open)
	}
	systray.AddSeparator()

	mPrefs := systray.AddMenuItem("Preferences", "Per-machine settings (saved to "+cfgPath+")")
	mAutoSrv := mPrefs.AddSubMenuItemCheckbox("Auto-start server on launch", "Start HTTP server immediately when tray opens", userCfg.AutoStartServer)
	mAutoWrk := mPrefs.AddSubMenuItemCheckbox("Auto-start worker on launch", "Start background worker immediately when tray opens", userCfg.AutoStartWorker)
	mAutoUpd := mPrefs.AddSubMenuItemCheckbox("Auto-update", "Check + download new releases in background", userCfg.AutoUpdate)
	mPrefs.AddSubMenuItem("─────────────", "").Disable()
	mOpenCfg := mPrefs.AddSubMenuItem("Open config file", "Open "+cfgPath)
	if cfgPath == "" {
		mOpenCfg.Disable()
	}
	systray.AddSeparator()

	mQuit := systray.AddMenuItem("Quit", "Quit "+appName)

	setServerLabel := func(running bool) {
		if running {
			mServer.SetTitle(fmt.Sprintf("Stop server  (running on :%d)", serverPort))
		} else {
			mServer.SetTitle("Start server")
		}
	}
	setWorkerLabel := func(running bool) {
		if running {
			mWorker.SetTitle("Stop worker  (running)")
		} else {
			mWorker.SetTitle("Start worker")
		}
	}

	if userCfg.AutoStartServer {
		if err := startServer(); err != nil {
			log.Printf("auto-start server: %v", err)
			setServerLabel(false)
		} else {
			setServerLabel(true)
		}
	} else {
		setServerLabel(false)
	}
	if userCfg.AutoStartWorker {
		if err := startWorker(); err != nil {
			log.Printf("auto-start worker: %v", err)
			setWorkerLabel(false)
		} else {
			setWorkerLabel(true)
		}
	} else {
		setWorkerLabel(false)
	}
	refreshIcon()

	go func() {
		for {
			select {
			case <-mServer.ClickedCh:
				if isServerRunning() {
					stopServer()
					setServerLabel(false)
				} else if err := startServer(); err != nil {
					log.Printf("start server: %v", err)
				} else {
					setServerLabel(true)
				}
				refreshIcon()
			case <-mWorker.ClickedCh:
				if isWorkerRunning() {
					stopWorker()
					setWorkerLabel(false)
				} else if err := startWorker(); err != nil {
					log.Printf("start worker: %v", err)
				} else {
					setWorkerLabel(true)
				}
				refreshIcon()
			case <-mLogs.ClickedCh:
				if logPath != "" {
					if err := openInEditor(logPath); err != nil {
						log.Printf("open logs: %v", err)
					}
				}
			case <-mInstallAll.ClickedCh:
				for _, ui := range uis {
					if err := installOne(ui.c); err != nil {
						log.Printf("install %s: %v", ui.c.ID, err)
					} else {
						ui.refresh()
					}
				}
			case <-mUninstallAll.ClickedCh:
				for _, ui := range uis {
					if err := mcpconfig.Uninstall(ui.c, appName); err != nil {
						log.Printf("uninstall %s: %v", ui.c.ID, err)
					} else {
						ui.refresh()
					}
				}
			case <-mAutoSrv.ClickedCh:
				userCfg.AutoStartServer = !userCfg.AutoStartServer
				if userCfg.AutoStartServer {
					mAutoSrv.Check()
				} else {
					mAutoSrv.Uncheck()
				}
				saveUserCfg()
			case <-mAutoWrk.ClickedCh:
				userCfg.AutoStartWorker = !userCfg.AutoStartWorker
				if userCfg.AutoStartWorker {
					mAutoWrk.Check()
				} else {
					mAutoWrk.Uncheck()
				}
				saveUserCfg()
			case <-mAutoUpd.ClickedCh:
				userCfg.AutoUpdate = !userCfg.AutoUpdate
				if userCfg.AutoUpdate {
					mAutoUpd.Check()
				} else {
					mAutoUpd.Uncheck()
				}
				saveUserCfg()
			case <-mOpenCfg.ClickedCh:
				if cfgPath != "" {
					if err := openInEditor(cfgPath); err != nil {
						log.Printf("open config: %v", err)
					}
				}
			case <-mExample.ClickedCh:
				path, err := writeExampleConfig()
				if err != nil {
					log.Printf("example config: %v", err)
					continue
				}
				if err := openInEditor(path); err != nil {
					log.Printf("open example: %v", err)
				}
			case <-mQuit.ClickedCh:
				stopServer()
				stopWorker()
				systray.Quit()
				return
			}
		}
	}()
}

func onExit() {
	stopServer()
	stopWorker()
}

func isServerRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	return serverCancel != nil
}

func isWorkerRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	return workerCancel != nil
}

func startServer() error {
	mu.Lock()
	if serverCancel != nil {
		mu.Unlock()
		return fmt.Errorf("already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	serverCancel = cancel
	serverDone = make(chan struct{})
	mu.Unlock()

	srv := api.NewServer()
	go func() {
		defer close(serverDone)
		if err := srv.Run(ctx, serverPort); err != nil {
			log.Printf("server: %v", err)
		}
		mu.Lock()
		serverCancel = nil
		mu.Unlock()
		refreshIcon()
	}()
	return nil
}

func stopServer() {
	mu.Lock()
	cancel := serverCancel
	done := serverDone
	mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}

func startWorker() error {
	mu.Lock()
	if workerCancel != nil {
		mu.Unlock()
		return fmt.Errorf("already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	workerCancel = cancel
	workerDone = make(chan struct{})
	mu.Unlock()

	srv := worker.NewServer()
	go func() {
		defer close(workerDone)
		if err := srv.Run(ctx); err != nil {
			log.Printf("worker: %v", err)
		}
		mu.Lock()
		workerCancel = nil
		mu.Unlock()
		refreshIcon()
	}()
	return nil
}

func stopWorker() {
	mu.Lock()
	cancel := workerCancel
	done := workerDone
	mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}

func installOne(c mcpconfig.Client) error {
	entry, err := mcpconfig.SelfEntry()
	if err != nil {
		return err
	}
	return mcpconfig.Install(c, appName, entry)
}

func writeExampleConfig() (string, error) {
	entry, err := mcpconfig.SelfEntry()
	if err != nil {
		return "", err
	}
	snippet := map[string]any{
		"mcpServers": map[string]any{appName: entry},
	}
	out, err := jsonIndent(snippet)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(os.TempDir(), appName+"-mcp-config.json")
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		return "", err
	}
	return dst, nil
}
