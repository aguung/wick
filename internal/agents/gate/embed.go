package gate

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed all:assets
var embeddedGateFS embed.FS

// envOverride is the env-var name dev tooling sets to point at a
// freshly-built `wick-gate` outside the embed (e.g. wicklab + go run).
// Resolution checks this first so VSCode F5 doesn't need a full
// rebuild of the parent binary.
const envOverride = "WICK_GATE_BIN"

// errNoEmbeddedGate signals that the embed is empty — typically a
// `go run` build that skipped the CI step. Caller can fall back to
// PATH lookup or surface the misconfiguration.
var errNoEmbeddedGate = errors.New("no embedded wick-gate for this platform")

// embeddedGateName returns the asset filename for the current
// runtime. Format mirrors the CI build step: assets/wick-gate-<os>-<arch>[.exe].
func embeddedGateName() string {
	name := fmt.Sprintf("assets/wick-gate-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// Resolution source labels — exposed via ResolveGateBinaryWithSource
// so the Providers page UI can show *how* the binary got picked,
// useful when debugging why dev override silently shadowed the
// embedded one.
const (
	SourceEnvOverride = "env_override"
	SourceEmbed       = "embed"
	SourceSibling     = "sibling"
	SourcePath        = "path"
)

// ResolveGateBinary picks the wick-gate binary for the current
// process. Thin wrapper around ResolveGateBinaryWithSource for
// callers that don't care about the resolution source.
func ResolveGateBinary(sessionDir string) (string, error) {
	path, _, err := ResolveGateBinaryWithSource(sessionDir)
	return path, err
}

// ResolveGateBinaryWithSource resolves wick-gate and returns which
// step found it. Resolution order:
//
//  1. $WICK_GATE_BIN — dev override, no extraction needed
//  2. embedded asset, extracted into sessionDir/gate/wick-gate[.exe]
//  3. sibling-of-executable — `wick-gate[.exe]` next to the running
//     binary. Covers the common dev / installer case where the
//     parent ships a sidecar binary in the same folder (matches
//     how VSCode debug task `debug: prep` lays them out: both
//     `bin/wick-lab.exe` + `bin/wick-gate.exe`).
//  4. `wick-gate` on PATH — last-ditch fallback for source builds
//     where neither override nor embed nor sibling are populated
func ResolveGateBinaryWithSource(sessionDir string) (path, source string, err error) {
	if p := strings.TrimSpace(os.Getenv(envOverride)); p != "" {
		return p, SourceEnvOverride, nil
	}
	if p, err := extractEmbeddedGate(sessionDir); err == nil {
		return p, SourceEmbed, nil
	} else if !errors.Is(err, errNoEmbeddedGate) {
		return "", "", err
	}
	if p := siblingGateBinary(); p != "" {
		return p, SourceSibling, nil
	}
	if p, err := exec.LookPath("wick-gate"); err == nil {
		return p, SourcePath, nil
	}
	return "", "", fmt.Errorf("wick-gate not found: set %s, place wick-gate next to the parent binary, or build the parent with the embed step", envOverride)
}

// siblingGateBinary returns the absolute path to wick-gate sitting
// in the same directory as the currently-running executable. Empty
// string when the file isn't there or os.Executable lookup fails —
// caller falls through to PATH lookup.
func siblingGateBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	name := "wick-gate"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	candidate := filepath.Join(filepath.Dir(exe), name)
	if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
		return candidate
	}
	return ""
}

// extractEmbeddedGate writes the embedded binary into
// sessionDir/gate/wick-gate[.exe] and returns the absolute path.
// Idempotent: if the file already exists with the same size as the
// embed, the extract is skipped.
func extractEmbeddedGate(sessionDir string) (string, error) {
	name := embeddedGateName()
	data, err := embeddedGateFS.ReadFile(name)
	if err != nil {
		// Either the asset is missing or the FS is empty — both map
		// to "no embedded gate" so the caller falls back gracefully.
		if errors.Is(err, fs.ErrNotExist) {
			return "", errNoEmbeddedGate
		}
		return "", fmt.Errorf("read embedded gate %s: %w", name, err)
	}
	if len(data) == 0 {
		return "", errNoEmbeddedGate
	}

	gateDir := filepath.Join(sessionDir, "gate")
	if err := os.MkdirAll(gateDir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", gateDir, err)
	}
	out := filepath.Join(gateDir, "wick-gate")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}

	// Skip rewrite if the on-disk file already matches the embed —
	// avoids fighting Windows ACLs on every spawn.
	if st, err := os.Stat(out); err == nil && st.Size() == int64(len(data)) {
		return out, nil
	}

	if err := os.WriteFile(out, data, 0o755); err != nil {
		return "", fmt.Errorf("write %s: %w", out, err)
	}
	return out, nil
}
