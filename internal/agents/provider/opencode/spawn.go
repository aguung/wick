package opencode

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"

	provider "github.com/yogasw/wick/internal/agents/provider"
	"github.com/yogasw/wick/internal/agents/provider/procgroup"
	"github.com/yogasw/wick/internal/agents/skillsync"
	"github.com/yogasw/wick/internal/pkg/envscrub"
	"github.com/yogasw/wick/pkg/safeexec"
)

// Spawner spawns `opencode run --format json`, one process per turn.
type Spawner struct {
	Binary    string // empty → "opencode"
	ExtraArgs []string
	// MCPToken is the per-session credential for wick's MCP server.
	MCPToken string
}

// buildArgs is the argv minus the prompt (piped on stdin — run.ts reads a
// non-TTY stdin as the message).
func buildArgs(opt provider.SpawnOptions, extra []string, model string, modelInArgs bool) []string {
	args := []string{"run", "--format", "json", "--thinking", "--auto"}
	args = append(args, extra...)
	args = append(args, opt.ExtraArgs...)
	if model != "" && !modelInArgs {
		args = append(args, "--model", model)
	}
	if opt.ResumeID != "" {
		args = append(args, "--session", opt.ResumeID)
	}
	return args
}

// writeSoul writes the wick system prompt under the per-session dir.
func writeSoul(opt provider.SpawnOptions) string {
	soul := skillsync.AppendBuiltinCatalog(opt.Preset)
	dir := opt.SessionDir
	if dir == "" {
		dir = opt.Workspace
	}
	if soul == "" || dir == "" {
		return ""
	}
	d := filepath.Join(dir, ".opencode-wick")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return ""
	}
	p := filepath.Join(d, "soul.md")
	if err := os.WriteFile(p, []byte(soul), 0o644); err != nil {
		log.Warn().Err(err).Str("path", p).Msg("agents.spawn: opencode write soul.md failed")
		return ""
	}
	return p
}

// spawnEnv is the env wick adds for one spawn: the instance's data dir,
// the inline config, and the MCP values its placeholders expand to.
func spawnEnv(ins provider.Instance, soulPath, endpoint, token string, disable []string) ([]string, error) {
	env, err := provider.OpencodeEnv(ins)
	if err != nil {
		return nil, err
	}
	parsed, err := provider.ParseExtraMCP(ins.ExtraMCPServers)
	if err != nil {
		return nil, err
	}
	extras := make(map[string]map[string]any, len(parsed))
	for name, s := range parsed {
		extras[name] = s.OpencodeEntry()
	}
	mcp := mcpEnv(endpoint, token)
	// Blank the host's extra config sources (config/config.ts): wick's
	// inline layer is the only addition allowed, and OPENCODE_AUTO_SHARE
	// must not publish anything.
	env = append(env, "OPENCODE_CONFIG=", "OPENCODE_CONFIG_DIR=", "OPENCODE_AUTO_SHARE=false")
	env = append(env, configEnvVar+"="+configContent(mcp != nil, soulPath, extras, disable))
	return append(env, mcp...), nil
}

// Spawn starts one opencode run with opt.InitialMessage as the prompt.
func (s Spawner) Spawn(ctx context.Context, opt provider.SpawnOptions) (provider.Process, error) {
	bin := s.Binary
	if bin == "" {
		bin = "opencode"
	}
	resolved, err := safeexec.ResolveBin(bin)
	if err != nil {
		return nil, fmt.Errorf("opencode binary not found: %w", err)
	}
	bin = resolved

	ins := provider.Instance{Type: provider.TypeOpencode, Name: string(provider.TypeOpencode)}
	if opt.Instance != nil {
		ins = *opt.Instance
	}
	model, inArgs, err := resolveModel(ins, opt, append(append([]string{}, s.ExtraArgs...), opt.ExtraArgs...))
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	added, err := spawnEnv(ins, writeSoul(opt), mcpEndpointFromEnv(), s.MCPToken, foreignMCPNames(opt.Workspace, home))
	if err != nil {
		return nil, fmt.Errorf("opencode instance %s: %w", ins.Name, err)
	}

	args := buildArgs(opt, s.ExtraArgs, model, inArgs)
	execBin, execArgs, scopeUnit := opt.MemGuard.Wrap(bin, args, "opencode", opt.SpawnSeq)
	cmd := safeexec.CommandContext(ctx, execBin, execArgs...)
	cmd.Dir = opt.Workspace
	cmd.Env = append(envscrub.ScrubOSEnv(), opt.ExtraEnv...)
	// After the instance env: the account dir must not be overridable by a
	// stray XDG_DATA_HOME in Env, or login and spawn would part ways.
	cmd.Env = append(cmd.Env, added...)
	hideConsole(cmd)
	procgroup.Apply(cmd)

	addedEnv := provider.MaskSpawnEnv(append(append([]string{}, opt.ExtraEnv...), added...))

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
		Str("resume", opt.ResumeID).Msg("agents.spawn: starting (opencode)")
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start opencode: %w", err)
	}
	opt.MemGuard.BiasChild(cmd.Process.Pid)
	go func() {
		_, _ = io.WriteString(stdin, opt.InitialMessage)
		_ = stdin.Close()
	}()

	log.Info().Int("pid", cmd.Process.Pid).Str("scope", scopeUnit).Msg("agents.spawn: started (opencode)")
	return &process{cmd: cmd, stdout: stdout, env: addedEnv, scopeUnit: scopeUnit, realBin: bin, realArgv: args}, nil
}
