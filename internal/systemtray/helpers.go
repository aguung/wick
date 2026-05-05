//go:build !headless

package systemtray

import (
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// resolveDBPath determines the SQLite DB path and sets DATABASE_URL env
// so config.Load() picks it up before the server/worker start.
//
// Resolution order (first non-empty wins):
//  1. DATABASE_URL already set in env (user/CI explicit override)
//  2. customPath from userconfig.DatabasePath (user edited config.json)
//  3. <binary_dir>/wick.db  — when wick.yml exists next to the binary
//  4. <UserConfigDir>/<appName>/wick.db  — standalone / downloaded binary
func resolveDBPath(appName, customPath string) {
	if os.Getenv("DATABASE_URL") != "" {
		return // already explicitly set — don't touch
	}
	if customPath != "" {
		os.Setenv("DATABASE_URL", customPath)
		log.Printf("db: custom path %s", customPath)
		return
	}
	exe, err := os.Executable()
	if err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		binDir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(binDir, "wick.yml")); err == nil {
			dbPath := filepath.Join(binDir, "wick.db")
			os.Setenv("DATABASE_URL", dbPath)
			log.Printf("db: project mode %s", dbPath)
			return
		}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return
	}
	dbPath := filepath.Join(base, appName, "wick.db")
	os.Setenv("DATABASE_URL", dbPath)
	log.Printf("db: standalone mode %s", dbPath)
}

func openInEditor(path string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("cmd", "/c", "start", "", path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

func jsonIndent(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
