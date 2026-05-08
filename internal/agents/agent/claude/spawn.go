// Package claude is the Claude-CLI specific Spawner implementation.
//
// Why a sub-package: keeping CLI-specific argv / env / flag math out
// of the core agent package lets phase 6 add codex / gemini siblings
// (`agent/codex`, `agent/gemini`) without touching agent.go. Each CLI
// owns its own folder with its own spawner, parser wiring, and
// CLI-version regression tests.
//
// The agent package depends on the agent.Spawner interface; this
// package satisfies it via Spawner. Importers: pool/factory.go (or
// any future custom factory that wants claude).
package claude

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/yogasw/wick/internal/agents/agent"
)

// Spawner spawns the real `claude` CLI binary with stream-json output
// and the resume flag when a CLI session ID is available.
//
// Binary defaults to `claude` (PATH lookup); operators can override
// via the Binary field for non-standard installs.
type Spawner struct {
	Binary string // empty → "claude"
}

// Spawn starts the subprocess. Caller passes ctx so cancel kills the
// process; the returned Process.Kill() forces it down faster.
func (s Spawner) Spawn(ctx context.Context, opt agent.SpawnOptions) (agent.Process, error) {
	bin := s.Binary
	if bin == "" {
		bin = "claude"
	}
	args := []string{"--output-format", "stream-json", "--input-format", "stream-json"}
	if opt.ResumeID != "" {
		args = append(args, "--resume", opt.ResumeID)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = opt.Workspace
	cmd.Env = append(os.Environ(), opt.ExtraEnv...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	// Stderr is folded into the parent's stderr — claude doesn't
	// normally write stream-json to stderr, but if it does we want
	// the operator to see it rather than dropping it on the floor.
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start claude: %w", err)
	}
	return &process{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

// process implements agent.Process for a started claude subprocess.
type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

func (p *process) Stdout() io.Reader     { return p.stdout }
func (p *process) Stdin() io.WriteCloser { return p.stdin }
func (p *process) Wait() error           { return p.cmd.Wait() }

func (p *process) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}
