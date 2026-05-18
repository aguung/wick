package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yogasw/wick/internal/agents/workflow"
	"github.com/yogasw/wick/internal/agents/workflow/engine"
	"github.com/yogasw/wick/internal/agents/workflow/integration"
	"github.com/yogasw/wick/internal/agents/workflow/template"
)

// HTTPExecutor performs an HTTP request. Retry policy from n.Retry;
// default GET; parse_response raw|json|bytes.
type HTTPExecutor struct {
	client *http.Client
}

// NewHTTPExecutor builds the HTTP executor with a 30s default client.
func NewHTTPExecutor() *HTTPExecutor {
	return &HTTPExecutor{client: &http.Client{Timeout: 30 * time.Second}}
}

// httpSchema is the per-field schema reflected via integration.StructSchema
// for workflow_node_types. Single source of truth for AI consumers.
type httpSchema struct {
	Method        string `wick:"required;key=method;dropdown=GET|POST|PUT|PATCH|DELETE;desc=HTTP method"`
	URL           string `wick:"required;key=url;desc=Full URL, rendered as Go template"`
	Headers       string `wick:"key=headers;desc=YAML map of header key/value pairs (NOT a JSON string)"`
	Query         string `wick:"key=query;desc=YAML map of query params"`
	Body          string `wick:"key=body;textarea;desc=Request body as string. Use YAML block scalar | for multiline JSON. NOT a YAML map."`
	ParseResponse string `wick:"key=parse_response;dropdown=raw|json|bytes;desc=How to parse response body (default: raw)"`
	Timeout       string `wick:"key=timeout;desc=Request timeout e.g. 30s"`
}

// Descriptor exposes the schema + docs for the MCP catalog.
func (e *HTTPExecutor) Descriptor() engine.NodeDescriptor {
	return engine.NodeDescriptor{
		Description: "Make an HTTP request. URL/headers/query/body rendered as Go templates.",
		WhenToUse:   "Direct external API calls without a connector module.",
		Example:     "- id: call_api\n  type: http\n  method: POST\n  url: https://api.example.com/tickets\n  headers:\n    Content-Type: application/json\n  body: |\n    {\"title\": \"{{jsonEscape (index .Event.Payload \\\"text\\\")}}\"}",
		Schema:      integration.StructSchema(httpSchema{}),
		Output: map[string]string{
			"status":  "int — HTTP status code",
			"body":    "string — response body",
			"headers": "map[string]string — response headers",
		},
	}
}

// Execute runs the request described by node n.
func (e *HTTPExecutor) Execute(ctx context.Context, n workflow.Node, rc *workflow.RunContext) (workflow.NodeOutput, error) {
	rctx := rc.RenderCtx()
	method := strings.ToUpper(n.Method)
	if method == "" {
		method = http.MethodGet
	}
	urlStr, err := template.Render(n.URL, rctx)
	if err != nil {
		return workflow.NodeOutput{}, fmt.Errorf("url: %w", err)
	}
	if len(n.Query) > 0 {
		u, err := url.Parse(urlStr)
		if err != nil {
			return workflow.NodeOutput{}, fmt.Errorf("url parse: %w", err)
		}
		q := u.Query()
		for k, v := range n.Query {
			rv, err := template.Render(v, rctx)
			if err != nil {
				return workflow.NodeOutput{}, fmt.Errorf("query %q: %w", k, err)
			}
			q.Set(k, rv)
		}
		u.RawQuery = q.Encode()
		urlStr = u.String()
	}

	body := io.Reader(nil)
	if n.Body != "" {
		rb, err := template.Render(n.Body, rctx)
		if err != nil {
			return workflow.NodeOutput{}, fmt.Errorf("body: %w", err)
		}
		body = strings.NewReader(rb)
	}

	timeout := time.Duration(n.TimeoutSec) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	maxAttempts := 1
	backoff := time.Second
	if n.Retry != nil {
		if n.Retry.Max > 0 {
			maxAttempts = n.Retry.Max + 1
		}
		if n.Retry.BackoffSec > 0 {
			backoff = time.Duration(n.Retry.BackoffSec) * time.Second
		}
	}

	var resp *http.Response
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(cctx, method, urlStr, body)
		if err != nil {
			return workflow.NodeOutput{}, fmt.Errorf("http build: %w", err)
		}
		for k, v := range n.Headers {
			rv, err := template.Render(v, rctx)
			if err != nil {
				return workflow.NodeOutput{}, fmt.Errorf("header %q: %w", k, err)
			}
			req.Header.Set(k, rv)
		}
		resp, lastErr = e.client.Do(req)
		if lastErr == nil && resp.StatusCode < 500 {
			break
		}
		if resp != nil {
			_ = resp.Body.Close()
			resp = nil
		}
		if attempt < maxAttempts-1 {
			select {
			case <-cctx.Done():
				return workflow.NodeOutput{}, cctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	if lastErr != nil {
		return workflow.NodeOutput{}, fmt.Errorf("http exec: %w", lastErr)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return workflow.NodeOutput{}, fmt.Errorf("http read body: %w", err)
	}
	out := workflow.NodeOutput{
		Fields: map[string]any{
			"status":  resp.StatusCode,
			"headers": flattenHeaders(resp.Header),
			"body":    string(raw),
		},
	}
	switch n.ParseResponse {
	case "json", "":
		var v any
		err := json.Unmarshal(raw, &v)
		if err == nil {
			out.Fields["json"] = v
		} else if n.ParseResponse == "json" {
			return workflow.NodeOutput{}, fmt.Errorf("parse_response json: %w", err)
		}
	case "raw":
	case "bytes":
		out.Fields["bytes"] = raw
	}
	return out, nil
}

func flattenHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}
