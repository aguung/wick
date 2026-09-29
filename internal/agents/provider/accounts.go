package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yogasw/wick/internal/userconfig"
	"github.com/yogasw/wick/pkg/safeexec"
)

// accounts.go pins the account store of the CLIs whose instance = one
// account (omp, opencode). Spawn, login and usage MUST all resolve the
// store through these helpers: a login that wrote to a different profile
// or dir than the spawn reads from looks like "logged in" on the card and
// "not authenticated" in the chat.
//
//   - omp: `--profile <p>` isolates auth, sessions, settings and caches
//     under ~/.omp/profiles/<p>/agent (oh-my-pi docs/cli-reference.md,
//     packages/utils/src/dirs.ts).
//   - opencode: there is no dedicated data-dir env; opencode resolves its
//     data dir as $XDG_DATA_HOME/opencode via xdg-basedir and keeps
//     auth.json there (packages/core/src/global.ts,
//     packages/opencode/src/auth/index.ts). So the instance owns an
//     XDG_DATA_HOME of its own.

// OMPConfig is the omp-specific per-instance state.
type OMPConfig struct {
	// Profile is the `--profile` value. Never empty after Load.
	Profile string
}

// OpencodeConfig is the opencode-specific per-instance state.
type OpencodeConfig struct {
	// DataDir is the XDG_DATA_HOME opencode runs under. Never empty after
	// Load (unless the wick data dir itself cannot be resolved).
	DataDir string
}

// accountIsolated reports whether instances of t each own one account
// store that wick must pin explicitly.
func (t Type) accountIsolated() bool { return t == TypeOMP || t == TypeOpencode }

// ompProfileRe is omp's own profile-name rule (packages/utils/src/dirs.ts
// PROFILE_NAME_RE). A name outside it makes omp refuse to start.
var ompProfileRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

var ompProfileBad = regexp.MustCompile(`[^a-z0-9._-]+`)

// DefaultOMPProfile is the profile a new omp instance gets:
// "wick-<name>", folded into omp's accepted alphabet.
func DefaultOMPProfile(instanceName string) string {
	n := ompProfileBad.ReplaceAllString(strings.ToLower(strings.TrimSpace(instanceName)), "-")
	n = strings.Trim(n, "-.")
	if n == "" {
		n = "default"
	}
	p := "wick-" + n
	if len(p) > 64 {
		p = strings.TrimRight(p[:64], "-.")
	}
	return p
}

// ValidOMPProfile reports whether omp accepts p as a profile name.
func ValidOMPProfile(p string) bool { return ompProfileRe.MatchString(p) }

// DefaultOpencodeDataDir is <wick data>/providers/opencode/<name>.
func DefaultOpencodeDataDir(instanceName string) (string, error) {
	root, err := userconfig.Dir(AppName())
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(instanceName)
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", fmt.Errorf("opencode: unusable instance name %q for a data dir", instanceName)
	}
	return filepath.Join(root, "providers", "opencode", name), nil
}

// OMPProfile is the profile spawn/login/usage must pass for ins.
func OMPProfile(ins Instance) string {
	if ins.OMPConfig != nil && strings.TrimSpace(ins.OMPConfig.Profile) != "" {
		return strings.TrimSpace(ins.OMPConfig.Profile)
	}
	return DefaultOMPProfile(ins.Name)
}

// OMPProfileArgs is the argv prefix selecting ins's profile. omp reads
// --profile before anything else (profile bootstrap), so callers put it
// first.
func OMPProfileArgs(ins Instance) []string { return []string{"--profile", OMPProfile(ins)} }

// OpencodeDataDir is the data dir spawn/login/`auth list` must use for ins.
func OpencodeDataDir(ins Instance) (string, error) {
	if ins.OpencodeConfig != nil && strings.TrimSpace(ins.OpencodeConfig.DataDir) != "" {
		return strings.TrimSpace(ins.OpencodeConfig.DataDir), nil
	}
	return DefaultOpencodeDataDir(ins.Name)
}

// OpencodeEnv returns the env entry pointing opencode at ins's data dir,
// creating the dir (0700 — it will hold auth.json) when missing.
func OpencodeEnv(ins Instance) ([]string, error) {
	dir, err := OpencodeDataDir(ins)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("opencode data dir %s: %w", dir, err)
	}
	return []string{"XDG_DATA_HOME=" + dir}, nil
}

// OpencodeAuthFile is where opencode keeps ins's credentials.
func OpencodeAuthFile(ins Instance) (string, error) {
	dir, err := OpencodeDataDir(ins)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "opencode", "auth.json"), nil
}

// applyAccountConfig fills the resolved account store onto ins from the
// persisted values (empty = default for the current name).
func applyAccountConfig(ins *Instance, ompProfile, opencodeDir string) {
	switch ins.Type {
	case TypeOMP:
		p := strings.TrimSpace(ompProfile)
		if p == "" {
			p = DefaultOMPProfile(ins.Name)
		}
		ins.OMPConfig = &OMPConfig{Profile: p}
	case TypeOpencode:
		d := strings.TrimSpace(opencodeDir)
		if d == "" {
			d, _ = DefaultOpencodeDataDir(ins.Name)
		}
		ins.OpencodeConfig = &OpencodeConfig{DataDir: d}
	}
}

// accountConfigToUser is the persisted form of ins's account store.
func accountConfigToUser(ins Instance) (ompProfile, opencodeDir string) {
	switch ins.Type {
	case TypeOMP:
		return OMPProfile(ins), ""
	case TypeOpencode:
		d, _ := OpencodeDataDir(ins)
		return "", d
	}
	return "", ""
}

// binaryOnPath reports whether bin resolves on PATH.
func binaryOnPath(bin string) bool {
	_, err := safeexec.LookPath(bin)
	return err == nil
}
