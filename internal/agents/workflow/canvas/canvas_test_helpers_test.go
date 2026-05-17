package canvas

import (
	"errors"

	"github.com/yogasw/wick/internal/agents/workflow"
)

// stubService is a minimal in-memory Service implementation for canvas tests.
type stubService struct {
	workflows map[string]workflow.Workflow
	loadErr   error
	updateErr error
}

func (s *stubService) Load(slug string) (workflow.Workflow, error) {
	if s.loadErr != nil {
		return workflow.Workflow{}, s.loadErr
	}
	w, ok := s.workflows[slug]
	if !ok {
		return workflow.Workflow{}, errors.New("not found: " + slug)
	}
	return w, nil
}

func (s *stubService) Update(slug string, w workflow.Workflow, _ map[string][]byte) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.workflows[slug] = w
	return nil
}

func (s *stubService) List() ([]string, error)                                         { return nil, nil }
func (s *stubService) Create(_ string, _ workflow.Workflow, _ map[string][]byte) error { return nil }
func (s *stubService) Delete(_ string) error                                           { return nil }
func (s *stubService) Toggle(_ string, _ bool) error                                   { return nil }
func (s *stubService) LoadDraft(_ string) (workflow.Workflow, error) {
	return workflow.Workflow{}, nil
}
func (s *stubService) HasDraft(_ string) bool                                  { return false }
func (s *stubService) SaveDraft(_ string, _ workflow.Workflow) error           { return nil }
func (s *stubService) Publish(_ string) (workflow.Workflow, error)             { return workflow.Workflow{}, nil }
func (s *stubService) DiscardDraft(_ string) error                             { return nil }
func (s *stubService) ListFiles(_ string) ([]string, error)                    { return nil, nil }
func (s *stubService) ReadFile(_, _ string) ([]byte, error)                    { return nil, nil }
func (s *stubService) WriteFile(_, _ string, _ []byte) error                   { return nil }
func (s *stubService) DeleteFile(_, _ string) error                            { return nil }
func (s *stubService) LoadState(_ string) (workflow.WorkflowState, error)      { return workflow.WorkflowState{}, nil }
func (s *stubService) SaveState(_ string, _ workflow.WorkflowState) error      { return nil }
func (s *stubService) LoadEnvValues(_ string) (map[string]string, error)       { return nil, nil }
func (s *stubService) SaveEnvValues(_ string, _ map[string]string) error       { return nil }
func (s *stubService) BaseDir() string                                         { return "" }

// newStub creates a stubService pre-loaded with minimal valid workflows.
func newStub(slugs ...string) *stubService {
	s := &stubService{workflows: map[string]workflow.Workflow{}}
	for _, slug := range slugs {
		s.workflows[slug] = minimalWorkflow(slug)
	}
	return s
}

// minimalWorkflow builds the smallest workflow that passes parse.Validate.
func minimalWorkflow(slug string) workflow.Workflow {
	return workflow.Workflow{
		ID:      slug,
		Name:    slug,
		Enabled: false,
		Triggers: []workflow.Trigger{
			{Type: workflow.TriggerManual, EntryNode: "start"},
		},
		Graph: workflow.Graph{
			Nodes: []workflow.Node{
				{ID: "start", Type: workflow.NodeShell, Command: []string{"echo", "hi"}},
			},
		},
	}
}

func newCanvas(svc *stubService) *Canvas { return New(svc) }

func hasEdge(edges []workflow.Edge, from, to string) bool {
	for _, e := range edges {
		if e.From == from && e.To == to {
			return true
		}
	}
	return false
}
