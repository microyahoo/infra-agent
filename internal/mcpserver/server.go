// Package mcpserver exposes the tools registry as MCP tools.
//
// It does not participate in any diagnostic decisions: it only forwards
// arguments, wraps results, and classifies errors.
// The tools registry is built on first call so that clients can list tools
// without connecting to a cluster.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/microyahoo/infra-agent/internal/access"
	"github.com/microyahoo/infra-agent/internal/ceph/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Provider supplies the tools registry on demand.
type Provider interface {
	Registry(ctx context.Context) (*tools.Registry, error)
	// Reset signals that the current connection is broken; next call should reconnect.
	Reset()
}

// New builds an MCP server with all 17 diagnostic tools registered.
//
// Tool names, descriptions, and input schemas are taken from a static registry
// (constructed with a nil runner) so that clients can call tools/list without
// triggering an SSH connection. Actual execution is delegated to the Provider.
func New(p Provider, version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "infra-agent", Version: version}, nil)
	static := tools.RegistryFrom(tools.All(nil, ""))
	for _, spec := range static.Specs() {
		srv.AddTool(&mcp.Tool{
			Name:        spec.Name,
			Description: spec.Description,
			InputSchema: spec.Schema,
		}, handlerFor(p, spec.Name))
	}
	return srv
}

func handlerFor(p Provider, name string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		reg, err := p.Registry(ctx)
		if err != nil {
			return errorResult(fmt.Errorf("接入集群失败: %w", err)), nil
		}
		args := req.Params.Arguments
		if args == nil {
			args = json.RawMessage(`{}`)
		}
		res, _, err := reg.Execute(ctx, name, args)
		if err != nil {
			if isConnError(err) {
				p.Reset()
			}
			return errorResult(err), nil
		}

		payload := map[string]any{"summary": res.Summary}
		if res.Data != nil {
			payload["data"] = res.Data
		}
		if len(res.Hints) > 0 {
			payload["hints"] = res.Hints
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return errorResult(err), nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
		}, nil
	}
}

func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}

// isConnError reports whether the error is worth reconnecting over.
// Parse errors do not warrant dropping a good connection.
func isConnError(err error) bool {
	var te *access.ToolError
	if errors.As(err, &te) {
		return te.Kind == access.KindUnreachable || te.Kind == access.KindTimeout
	}
	return false
}
