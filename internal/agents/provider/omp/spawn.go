package omp

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"

	provider "github.com/yogasw/wick/internal/agents/provider"
	"github.com/yogasw/wick/internal/agents/provider/procgroup"
	"github.com/yogasw/wick/internal/agents/skillsync"
	"github.com/yogasw/wick/internal/pkg/envscrub"
	"github.com/yogasw/wick/pkg/safeexec"
)

// Spawner spawns `omp` in print/json mode, one process per turn.
type Spawner struct {
	Binary    string // empty → "omp"
	ExtraArgs []string
	// MCPToken is the per-session credential for wick's MCP server. Empty =
	// no wick tools (the mcp.json entry is left alone).
	MCPToken string
}

// homeDir is swapped in tests so mcp.json lands in a temp dir.
var homeDir = os.UserHomeDir

// buildArgs is the argv minus the prompt (which goes on stdin).
// --profile leads: omp resolves the profile before any other flag.
func buildArgs(ins provider.Instance, opt provider.SpawnOptions, soulPath, overlayPath string, extra []string) []string {
	args := provider.OMPProfileArgs(ins)
	args = append(args, "-p", "--mode", "json", "--no-title", "--auto-approve")
	if overlayPath != "" {
		// Settings overlay that keeps host MCP out (see isolationOverlay).
		args = append(args, "--config", overlayPath)
	}
	if opt.Workspace != "" {
		args = append(args, "--cwd", opt.Workspace)
	}
	if soulPath != "" {
		// A value without a newline that names a readable file is read as
		// that file (system-prompt.ts resolvePromptInput).
		args = append(args, "--append-system-prompt", soulPath)
	}
	args = append(args, extra...)
	args = append(args, opt.ExtraArgs...)
	args = append(args, provider.ModelArgs(opt, args)...)
	if opt.ResumeID != "" {
		args = append(args, "--resume", opt.ResumeID)
	}
	return args
}

// writeSoul writes the wick system prompt (preset + shipped-skill catalog)
// under the per-session dir — never the shared workspace, see codex/spawn.go.
func writeSoul(opt provider.SpawnOptions) string {
	soul := skillsync.AppendBuiltinCatalog(opt.Preset)
	dir := opt.SessionDir
	if dir == "" {
		dir = opt.Workspace
	}
	if soul == "" || dir == "" {
		return ""
	}
	d := filepath.Join(dir, ".omp")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return ""
	}
	p := filepath.Join(d, "soul.md")
	if err := os.WriteFile(p, []byte(soul), 0o644); err != nil {
		log.Warn().Err(err).Str("path", p).Msg("agents.spawn: omp write soul.md failed")
		return ""
	}
	return p
}

// writeOverlay writes the isolation overlay under the per-session dir and
// returns its path ("" when there is nowhere to put it).
func writeOverlay(opt provider.SpawnOptions) string {
	dir := opt.SessionDir
	if dir == "" {
		dir = opt.Workspace
	}
	if dir == "" {
		return ""
	}
	d := filepath.Join(dir, ".omp")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return ""
	}
	p := filepath.Join(d, "wick-settings.yml")
	if err := os.WriteFile(p, isolationOverlay(), 0o644); err != nil {
		log.Warn().Err(err).Str("path", p).Msg("agents.spawn: omp write settings overlay failed")
		return ""
	}
	return p
}

// extraMCPEntries converts the instance's extra MCP servers to omp's shape.
func extraMCPEntries(ins provider.Instance) (map[string]map[string]any, error) {
	parsed, err := provider.ParseExtraMCP(ins.ExtraMCPServers)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]any, len(parsed))
	for name, s := range parsed {
		out[name] = s.OMPEntry()
	}
	return out, nil
}

// envValue returns the last KEY=value for key in env, else "".
func envValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			v = kv[len(key)+1:]
		}
	}
	return v
}

// Spawn starts one omp run with opt.InitialMessage as the prompt.
func (s Spawner) Spawn(ctx context.Context, opt provider.SpawnOptions) (provider.Process, error) {
	bin := s.Binary
	if bin == "" {
		bin = "omp"
	}
	resolved, err := safeexec.ResolveBin(bin)
	if err != nil {
		return nil, fmt.Errorf("omp binary not found: %w", err)
	}
	bin = resolved

	ins := provider.Instance{Type: provider.TypeOMP, Name: string(provider.TypeOMP)}
	if opt.Instance != nil {
		ins = *opt.Instance
	}
	profile := provider.OMPProfile(ins)
	if !provider.ValidOMPProfile(profile) {
		return nil, fmt.Errorf("omp instance %s: profile %q is not a valid omp profile name", ins.Name, profile)
	}

	// The profile's mcp.json holds wick's per-session entry (placeholders,
	// values in env) plus the instance's extra servers — and nothing else
	// wick put there before.
	extras, err := extraMCPEntries(ins)
	if err != nil {
		return nil, fmt.Errorf("omp instance %s: %w", ins.Name, err)
	}
	var mcpVars []string
	endpoint := mcpEndpointFromEnv()
	withWick := endpoint != "" && s.MCPToken != ""
	if home, _ := homeDir(); home != "" {
		cfgDir := envValue(opt.ExtraEnv, "PI_CONFIG_DIR")
		if cfgDir == "" {
			cfgDir = os.Getenv("PI_CONFIG_DIR")
		}
		if err := ensureMCPConfig(profileAgentDir(home, cfgDir, profile), withWick, extras); err != nil {
			log.Warn().Err(err).Str("profile", profile).Msg("agents.spawn: omp mcp.json not updated — wick tools unavailable")
		} else if withWick {
			mcpVars = mcpEnv(endpoint, s.MCPToken)
		}
	}

	args := buildArgs(ins, opt, writeSoul(opt), writeOverlay(opt), s.ExtraArgs)

	execBin, execArgs, scopeUnit := opt.MemGuard.Wrap(bin, args, "omp", opt.SpawnSeq)
	cmd := safeexec.CommandContext(ctx, execBin, execArgs...)
	cmd.Dir = opt.Workspace
	cmd.Env = append(envscrub.ScrubOSEnv(), opt.ExtraEnv...)
	cmd.Env = append(cmd.Env, mcpVars...)
	// omp turns ~/.claude on as an MCP/config source whenever
	// CLAUDE_CONFIG_DIR is set (capability/index.ts isUserSourceEnabled),
	// and PI_CONFIG_FILES adds config overlays — neither may leak in from
	// wick's own environment.
	cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR=", "PI_CONFIG_FILES=")
	hideConsole(cmd)
	procgroup.Apply(cmd)

	addedEnv := provider.MaskSpawnEnv(append(append([]string{}, opt.ExtraEnv...), mcpVars...))

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	log.Info().Str("bin", bin).Strs("argv", args).Str("cwd", opt.Workspace).
		Str("resume", opt.ResumeID).Str("profile", profile).Msg("agents.spawn: starting (omp)")
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start omp: %w", err)
	}
	opt.MemGuard.BiasChild(cmd.Process.Pid)
	// omp reads a non-TTY stdin to EOF as the prompt (main.ts readPipedInput).
	go writePrompt(stdin, opt.InitialMessage)

	log.Info().Int("pid", cmd.Process.Pid).Str("scope", scopeUnit).Msg("agents.spawn: started (omp)")
	return &process{cmd: cmd, stdout: stdout, env: addedEnv, scopeUnit: scopeUnit, realBin: bin, realArgv: args}, nil
}

func writePrompt(w io.WriteCloser, prompt string) {
	_, _ = io.WriteString(w, prompt)
	_ = w.Close()
}
