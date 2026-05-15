package nodes

import (
	"context"
	"fmt"

	"github.com/yogasw/wick/internal/agents/workflow"
	"github.com/yogasw/wick/internal/agents/workflow/provider"
	"github.com/yogasw/wick/internal/agents/workflow/template"
)

// AgentExecutor invokes a provider's AgentCall for a `type: agent`
// node. Skills + tools allowlist passed through.
type AgentExecutor struct {
	Providers *provider.Registry
}

// NewAgentExecutor wires the executor.
func NewAgentExecutor(reg *provider.Registry) *AgentExecutor {
	return &AgentExecutor{Providers: reg}
}

// Execute runs the agent node via provider.AgentCall.
func (e *AgentExecutor) Execute(ctx context.Context, n workflow.Node, rc *workflow.RunContext) (workflow.NodeOutput, error) {
	if e.Providers == nil {
		return workflow.NodeOutput{}, fmt.Errorf("agent: no provider registry configured")
	}
	prov, err := e.Providers.Get(n.Provider)
	if err != nil {
		return workflow.NodeOutput{}, err
	}
	prompt, err := template.Render(n.Prompt, rc.RenderCtx())
	if err != nil {
		return workflow.NodeOutput{}, err
	}
	if err := validateSkills(ctx, prov, n.Skills); err != nil {
		return workflow.NodeOutput{}, err
	}
	req := provider.AgentRequest{
		Prompt:    prompt,
		Preset:    n.Preset,
		Workspace: n.Workspace,
		Skills:    n.Skills,
		Tools:     n.Tools,
		MaxTurns:  n.MaxTurns,
		SessionID: classifySessionID(n, rc),
	}
	res, err := prov.AgentCall(ctx, req)
	if err != nil {
		return workflow.NodeOutput{}, fmt.Errorf("agent call: %w", err)
	}
	return workflow.NodeOutput{
		Result: res.Text,
		Fields: map[string]any{
			"text":        res.Text,
			"tools_used":  res.ToolsUsed,
			"skills_used": res.SkillsUsed,
			"usage":       res.Usage,
		},
	}, nil
}

func validateSkills(ctx context.Context, prov provider.Provider, skills []string) error {
	if len(skills) == 0 {
		return nil
	}
	have, err := prov.ListSkills(ctx)
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, s := range have {
		set[s.Name] = true
	}
	for _, want := range skills {
		if !set[want] {
			return fmt.Errorf("agent skill %q not available on provider %q", want, prov.Name())
		}
	}
	return nil
}
