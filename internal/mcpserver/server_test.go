package mcpserver_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/internal/access"
	"github.com/microyahoo/infra-agent/internal/ceph/tools"
	"github.com/microyahoo/infra-agent/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type stubRunner struct{}

func (stubRunner) Run(_ context.Context, _ string, _ []string) (access.Output, error) {
	return access.Output{Stdout: `{"health":{"status":"HEALTH_OK"},"fsid":"fsid-test","monmap":{"num_mons":3},"osdmap":{"num_osds":1,"num_up_osds":1,"num_in_osds":1},"pgmap":{"num_pgs":4}}`}, nil
}
func (stubRunner) Capabilities(_ context.Context, _ string) access.Capabilities {
	return access.Capabilities{CephCLI: true}
}
func (stubRunner) Topology(_ context.Context) (*access.Topology, error) {
	return &access.Topology{}, nil
}
func (stubRunner) Nodes() []string { return []string{"node-1"} }

type fakeProvider struct {
	reg *tools.Registry
	err error
}

func (p *fakeProvider) Registry(_ context.Context) (*tools.Registry, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.reg, nil
}
func (p *fakeProvider) Reset() {}

func toolsRegistry() *tools.Registry {
	return tools.RegistryFrom(tools.All(nil, ""))
}

func connect(t *testing.T, p mcpserver.Provider) *mcp.ClientSession {
	t.Helper()
	srv := mcpserver.New(p, "test")
	st1, st2 := mcp.NewInMemoryTransports()

	ctx := context.Background()
	if _, err := srv.Connect(ctx, st1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, st2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	return cs
}

func TestListsAllTools(t *testing.T) {
	cs := connect(t, &fakeProvider{reg: toolsRegistry()})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 17 {
		names := make([]string, len(res.Tools))
		for i, tool := range res.Tools {
			names[i] = tool.Name
		}
		t.Fatalf("expected 17 tools, got %d: %v", len(res.Tools), names)
	}
	for _, tool := range res.Tools {
		if tool.Name == "" || tool.Description == "" || tool.InputSchema == nil {
			t.Errorf("incomplete tool definition: %+v", tool)
		}
	}
}

func TestCallsToolThroughProvider(t *testing.T) {
	reg := tools.RegistryFrom([]tools.Tool{tools.NewCephStatus(stubRunner{})})
	cs := connect(t, &fakeProvider{reg: reg})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ceph_status"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}
	body, _ := json.Marshal(res.Content)
	if !strings.Contains(string(body), "HEALTH_OK") {
		t.Errorf("result should contain health: %s", body)
	}
}

func TestProviderFailureIsToolError(t *testing.T) {
	cs := connect(t, &fakeProvider{err: context.DeadlineExceeded})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ceph_status"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected IsError when the provider fails")
	}
}
