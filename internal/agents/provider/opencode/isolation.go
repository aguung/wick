package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	provider "github.com/yogasw/wick/internal/agents/provider"
)

// isolation.go keeps an opencode spawn to wick's MCP + the instance's own
// extras, and to a model someone actually chose.
//
// Config layers opencode merges (packages/opencode/src/config/config.ts):
// remote well-known (per auth entry — lives in the instance data dir),
// global Global.Path.config (moved per instance via XDG_CONFIG_HOME, see
// provider.OpencodeEnv), OPENCODE_CONFIG (blanked), project
// opencode.json(c) walking up from the cwd, `.opencode/` dirs up from the
// cwd and ~/.opencode (config/paths.ts), OPENCODE_CONFIG_DIR (blanked),
// then OPENCODE_CONFIG_CONTENT — ours, merged last.
//
// OPENCODE_DISABLE_PROJECT_CONFIG would drop the project layers, but it
// also drops the project's AGENTS.md rules (session/instruction.ts:81), so
// instead wick reads those files itself and, in its own last-merged layer,
// sets every MCP server they declare to enabled:false.

// jsoncComment strips // and /* */ comments outside strings; trailing
// commas are removed afterwards. Enough for config files, not a full JSONC.
var trailingComma = regexp.MustCompile(`,(\s*[}\]])`)

func stripJSONC(b []byte) []byte {
	var out []byte
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(b) && b[i+1] == '/' {
			for i < len(b) && b[i] != '\n' {
				i++
			}
			out = append(out, '\n')
			continue
		}
		if c == '/' && i+1 < len(b) && b[i+1] == '*' {
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		out = append(out, c)
	}
	return trailingComma.ReplaceAll(out, []byte("$1"))
}

// configMCPNames returns the MCP server names one config file declares.
func configMCPNames(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		MCP map[string]json.RawMessage `json:"mcp"`
	}
	if json.Unmarshal(stripJSONC(b), &doc) != nil {
		return nil
	}
	out := make([]string, 0, len(doc.MCP))
	for k := range doc.MCP {
		out = append(out, k)
	}
	return out
}

// foreignMCPNames lists every MCP server name the non-wick config layers
// would bring into a spawn in workspace: opencode.json(c) and
// .opencode/opencode.json(c) from workspace up to /, plus ~/.opencode.
func foreignMCPNames(workspace, home string) []string {
	seen := map[string]bool{}
	add := func(dir string) {
		for _, f := range []string{"opencode.json", "opencode.jsonc", filepath.Join(".opencode", "opencode.json"), filepath.Join(".opencode", "opencode.jsonc")} {
			for _, n := range configMCPNames(filepath.Join(dir, f)) {
				seen[n] = true
			}
		}
	}
	if workspace != "" {
		dir := filepath.Clean(workspace)
		for {
			add(dir)
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if home != "" {
		for _, f := range []string{"opencode.json", "opencode.jsonc"} {
			for _, n := range configMCPNames(filepath.Join(home, ".opencode", f)) {
				seen[n] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ErrNoModel is returned when a spawn has no model to pass.
var ErrNoModel = errors.New("no model")

// resolveModel picks the --model value: a session pin, then --model/-m in
// the instance args, then the instance's opencode_model. opencode without
// --model silently uses its hosted default (opencode/…), sending the
// conversation to opencode's servers, so an empty result refuses the spawn.
// "opencode/…" (hosted) models additionally need AllowHosted.
func resolveModel(ins provider.Instance, opt provider.SpawnOptions, args []string) (model string, fromArgs bool, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "--model" || a == "-m") && i+1 < len(args):
			model, fromArgs = args[i+1], true
		case strings.HasPrefix(a, "--model="):
			model, fromArgs = strings.TrimPrefix(a, "--model="), true
		}
	}
	if !fromArgs {
		if m := provider.ModelArgs(opt, nil); len(m) == 2 {
			model = m[1]
		} else if ins.OpencodeConfig != nil {
			model = strings.TrimSpace(ins.OpencodeConfig.Model)
		}
	}
	allowHosted := ins.OpencodeConfig != nil && ins.OpencodeConfig.AllowHosted
	if model == "" {
		if !hasCredentials(ins) {
			return "", false, fmt.Errorf("opencode instance %s: log in first (Providers → Connection) or pick a model (opencode_model) — refusing to fall back to opencode's hosted default: %w", ins.Name, ErrNoModel)
		}
		return "", false, fmt.Errorf("opencode instance %s: pick a model (opencode_model, e.g. openai/gpt-5.5) — without one opencode silently uses its hosted default: %w", ins.Name, ErrNoModel)
	}
	if strings.HasPrefix(model, "opencode/") && !allowHosted {
		return "", false, fmt.Errorf("opencode instance %s: model %s is opencode's hosted service (conversation goes to opencode's servers); enable opencode_allow_hosted on the instance to allow it", ins.Name, model)
	}
	return model, fromArgs, nil
}

// hasCredentials reports whether the instance's auth.json holds any login.
func hasCredentials(ins provider.Instance) bool {
	p, err := provider.OpencodeAuthFile(ins)
	if err != nil {
		return false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	var m map[string]json.RawMessage
	return json.Unmarshal(b, &m) == nil && len(m) > 0
}
