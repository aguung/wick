// Package updater downloads, verifies, and applies new release
// binaries from GitHub. The current binary embeds a target release
// repo + PAT (set at build time via `wick build --github-repo ...`
// and `--github-pat ...`); at startup the system tray asks this
// package whether a staged update is pending (apply + re-exec) or
// whether to fetch a newer release in the background.
//
// Asset naming convention (must match the release CI workflow):
//
//	<appName>-<GOOS>-<GOARCH>[.exe]            binary
//	<appName>-<GOOS>-<GOARCH>[.exe].sha256     checksum sibling
//
// Repo resolution:
//
//	1. repoFull arg ("owner/repo"), typically baked from --github-repo
//	2. fallback to debug.ReadBuildInfo() Main.Path when arg is empty
//	   (lets a "same source repo as releases" setup work without a flag)
//	3. else updater is disabled — Configured() returns false and
//	   CheckNow returns an error.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/mod/semver"

	"github.com/yogasw/wick/internal/userconfig"
)

const (
	githubAPI   = "https://api.github.com"
	httpTimeout = 60 * time.Second
)

// Updater is safe for concurrent use. CheckNow guards itself with a
// "in flight" flag so background and manual triggers don't double-fire.
type Updater struct {
	appName        string
	currentVersion string
	owner, repo    string
	pat            string
	cacheDir       string

	cfg     *userconfig.Config
	saveCfg func() error

	mu       sync.Mutex
	checking bool
}

// Result is what CheckNow returns to the caller. The tray uses
// Downloaded to show "Restart now (vX)" and AlreadyLatest to log a
// quiet "you're current" line for a manual click.
type Result struct {
	LatestVersion string
	Downloaded    bool
	AlreadyLatest bool
}

// New constructs an Updater. cfg + save let the updater persist
// staged-update state into the same user-config file the tray uses
// for its other prefs, so a quit-and-relaunch picks up the staged
// binary without re-downloading.
func New(cfg *userconfig.Config, save func() error, appName, currentVersion, repoFull, pat string) (*Updater, error) {
	if cfg == nil || save == nil {
		return nil, errors.New("updater: cfg and save are required")
	}
	owner, repo := parseRepo(repoFull)
	if owner == "" {
		owner, repo = parseRepo(moduleRepo())
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("user cache dir: %w", err)
	}
	cache := filepath.Join(base, appName, "updates")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir cache: %w", err)
	}
	return &Updater{
		appName:        appName,
		currentVersion: normalizeVer(currentVersion),
		owner:          owner,
		repo:           repo,
		pat:            pat,
		cacheDir:       cache,
		cfg:            cfg,
		saveCfg:        save,
	}, nil
}

// Configured reports whether a release source is known. False means
// the updater can't do anything — caller should hide UI affordances.
func (u *Updater) Configured() bool { return u.owner != "" && u.repo != "" }

// HasStaged returns true when a previously downloaded binary is still
// on disk and waiting to be applied. Stale config rows that point at
// a missing file are treated as no-staged (and should be cleared by
// the caller).
func (u *Updater) HasStaged() bool {
	return u.cfg.StagedUpdatePath != "" && fileExists(u.cfg.StagedUpdatePath)
}

// StagedVersion is the tag (e.g. "v1.2.3") of the pending update.
func (u *Updater) StagedVersion() string { return u.cfg.StagedUpdateVersion }

// CheckNow synchronously fetches the latest release, compares semver,
// and downloads + stages if newer. Idempotent: re-running with the
// same staged version is a no-op. Concurrent calls are coalesced —
// the second caller gets an "in progress" error.
func (u *Updater) CheckNow(ctx context.Context) (Result, error) {
	if !u.Configured() {
		return Result{}, errors.New("updater not configured (no github repo)")
	}
	u.mu.Lock()
	if u.checking {
		u.mu.Unlock()
		return Result{}, errors.New("check already in progress")
	}
	u.checking = true
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.checking = false
		u.mu.Unlock()
	}()

	rel, err := u.fetchLatest(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("fetch latest: %w", err)
	}
	latest := normalizeVer(rel.TagName)
	if !semverNewer(latest, u.currentVersion) {
		return Result{LatestVersion: latest, AlreadyLatest: true}, nil
	}
	if u.cfg.StagedUpdateVersion == latest && fileExists(u.cfg.StagedUpdatePath) {
		return Result{LatestVersion: latest}, nil
	}

	bin, sum := u.pickAssets(rel.Assets)
	if bin == nil {
		return Result{}, fmt.Errorf("no asset matched %s", u.assetName())
	}

	binData, err := u.downloadAsset(ctx, bin.URL)
	if err != nil {
		return Result{}, fmt.Errorf("download: %w", err)
	}
	if sum != nil {
		sumData, err := u.downloadAsset(ctx, sum.URL)
		if err != nil {
			return Result{}, fmt.Errorf("download sha256: %w", err)
		}
		want := parseSHA256(string(sumData))
		got := sha256Hex(binData)
		if want == "" || !strings.EqualFold(want, got) {
			return Result{}, fmt.Errorf("sha256 mismatch (got %s, want %s)", got, want)
		}
	}

	stagedPath := filepath.Join(u.cacheDir, fmt.Sprintf("%s-%s%s", u.appName, latest, exeExt()))
	if err := os.WriteFile(stagedPath, binData, 0o755); err != nil {
		return Result{}, fmt.Errorf("write staged: %w", err)
	}
	u.cfg.StagedUpdatePath = stagedPath
	u.cfg.StagedUpdateVersion = latest
	if err := u.saveCfg(); err != nil {
		return Result{}, fmt.Errorf("save staged path: %w", err)
	}
	return Result{LatestVersion: latest, Downloaded: true}, nil
}

// ApplyStagedAndRestart performs the binary swap and re-execs the new
// process. Caller passes stop funcs (server cancel, worker cancel) so
// goroutines drain before the swap. On success this function does not
// return — Unix syscall.Exec replaces our image; Windows spawns a new
// process and we os.Exit. Returns an error only when the swap itself
// fails before re-exec.
func (u *Updater) ApplyStagedAndRestart(stops ...func()) error {
	if !u.HasStaged() {
		return errors.New("no staged update")
	}
	for _, s := range stops {
		s()
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	staged := u.cfg.StagedUpdatePath

	// Clear staged state BEFORE swap so a partial failure on next
	// launch doesn't loop us through a broken update forever.
	u.cfg.StagedUpdatePath = ""
	u.cfg.StagedUpdateVersion = ""
	if err := u.saveCfg(); err != nil {
		return fmt.Errorf("clear staged: %w", err)
	}

	if runtime.GOOS == "windows" {
		return swapWindows(exe, staged)
	}
	return swapUnix(exe, staged)
}

// swapUnix renames staged → current then re-execs in place. Atomic
// on the same filesystem; if cacheDir lives on a different mount the
// rename falls back to a copy + rename.
func swapUnix(current, staged string) error {
	if err := os.Chmod(staged, 0o755); err != nil {
		return fmt.Errorf("chmod staged: %w", err)
	}
	if err := os.Rename(staged, current); err != nil {
		if err := copyFile(staged, current); err != nil {
			return fmt.Errorf("rename + copy fallback: %w", err)
		}
		_ = os.Remove(staged)
	}
	args := append([]string{current}, os.Args[1:]...)
	return syscall.Exec(current, args, os.Environ())
}

// swapWindows can't overwrite a running .exe, so it renames the
// current exe to .old, moves the staged binary into place, spawns
// the new binary, and exits. The .old is best-effort; Windows GC's
// it on next reboot if still locked.
func swapWindows(current, staged string) error {
	old := current + ".old"
	_ = os.Remove(old)
	if err := os.Rename(current, old); err != nil {
		return fmt.Errorf("rename current → old: %w", err)
	}
	if err := os.Rename(staged, current); err != nil {
		_ = os.Rename(old, current)
		return fmt.Errorf("rename staged → current: %w", err)
	}
	cmd := exec.Command(current, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start new binary: %w", err)
	}
	os.Exit(0)
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// CleanupOldBinary removes any leftover <exe>.old from a prior
// Windows update swap. Safe to call from startup; quietly ignores
// "file in use" errors (Windows will purge on reboot).
func CleanupOldBinary() {
	if runtime.GOOS != "windows" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if err := os.Remove(exe + ".old"); err != nil && !os.IsNotExist(err) {
		log.Printf("updater: remove old binary: %v", err)
	}
}

// ----- GitHub API -----

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func (u *Updater) fetchLatest(ctx context.Context) (*ghRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", githubAPI, u.owner, u.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if u.pat != "" {
		req.Header.Set("Authorization", "Bearer "+u.pat)
	}
	resp, err := newClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("github auth failed (%d) — PAT_DOWNLOAD may be expired; rotate it and publish a new release", resp.StatusCode)
		}
		return nil, fmt.Errorf("github %d: %s", resp.StatusCode, string(body))
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func (u *Updater) downloadAsset(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	if u.pat != "" {
		req.Header.Set("Authorization", "Bearer "+u.pat)
	}
	resp, err := newClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("download %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}

func (u *Updater) pickAssets(assets []ghAsset) (bin, sum *ghAsset) {
	target := u.assetName()
	sumName := target + ".sha256"
	for i := range assets {
		a := &assets[i]
		switch a.Name {
		case target:
			bin = a
		case sumName:
			sum = a
		}
	}
	return
}

func (u *Updater) assetName() string {
	return fmt.Sprintf("%s-%s-%s%s", u.appName, runtime.GOOS, runtime.GOARCH, exeExt())
}

func exeExt() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// ----- helpers -----

func newClient() *http.Client { return &http.Client{Timeout: httpTimeout} }

func parseRepo(full string) (owner, repo string) {
	full = strings.TrimSpace(full)
	if full == "" {
		return "", ""
	}
	full = strings.TrimPrefix(full, "github.com/")
	parts := strings.Split(full, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

func moduleRepo() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return info.Main.Path
}

func normalizeVer(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "dev" || v == "unknown" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

func semverNewer(latest, current string) bool {
	if latest == "" {
		return false
	}
	if current == "" {
		// dev / unknown build — treat any tagged release as newer.
		return true
	}
	if !semver.IsValid(latest) || !semver.IsValid(current) {
		return false
	}
	return semver.Compare(latest, current) > 0
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// parseSHA256 reads a `sha256sum`-style line ("<64 hex>  filename")
// and returns the digest. Tolerates extra whitespace / a trailing
// newline; ignores everything after the first field.
func parseSHA256(text string) string {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
