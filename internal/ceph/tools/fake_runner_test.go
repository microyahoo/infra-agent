package tools

import (
	"context"
	"strings"

	"github.com/microyahoo/infra-agent/internal/access"
)

// fakeRunner 用预置响应模拟集群，使工具层测试不依赖真实机器。
type fakeRunner struct {
	responses map[string]access.Output // key: "node|ceph osd df -f json"
	errs      map[string]error
	caps      access.Capabilities
	topo      *access.Topology
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		responses: map[string]access.Output{},
		errs:      map[string]error{},
		caps: access.Capabilities{
			CephCLI: true, CephVersion: "18.2.4", Backend: "host-ceph",
			Kubectl: false, ContainerRT: "none",
			LogDirs:   []string{"/var/log/ceph"},
			HostTools: map[string]bool{"iostat": true, "smartctl": true, "lsblk": true, "dmesg": true},
		},
		topo: &access.Topology{
			FSID: "fsid-abc",
			Nodes: []access.TopoNode{
				{Name: "node-3", Host: "node-3", OSDIDs: []int{7, 8}},
				{Name: "node-4", Host: "node-4", OSDIDs: []int{9}},
			},
			OSDs: []access.OSDPlacement{
				{ID: 7, Host: "node-3", Device: "sdc", Status: "up"},
				{ID: 8, Host: "node-3", Device: "sdd", Status: "up"},
				{ID: 9, Host: "node-4", Device: "sdb", Status: "up"},
			},
		},
	}
}

// on 注册某节点某命令的输出。
func (f *fakeRunner) on(node string, argv []string, stdout string) *fakeRunner {
	f.responses[node+"|"+strings.Join(argv, " ")] = access.Output{Stdout: stdout, Node: node}
	return f
}

// fail 注册某节点某命令的错误。
func (f *fakeRunner) fail(node string, argv []string, err error) *fakeRunner {
	f.errs[node+"|"+strings.Join(argv, " ")] = err
	return f
}

func (f *fakeRunner) Run(_ context.Context, node string, argv []string) (access.Output, error) {
	key := node + "|" + strings.Join(argv, " ")
	if err, ok := f.errs[key]; ok {
		return access.Output{}, err
	}
	if out, ok := f.responses[key]; ok {
		return out, nil
	}
	return access.Output{}, &access.ToolError{Kind: access.KindUnavailable, Message: "not stubbed: " + key}
}

func (f *fakeRunner) Capabilities(_ context.Context, _ string) access.Capabilities {
	return f.caps
}

func (f *fakeRunner) Topology(_ context.Context) (*access.Topology, error) {
	return f.topo, nil
}

func (f *fakeRunner) Nodes() []string {
	out := make([]string, 0, len(f.topo.Nodes))
	for _, n := range f.topo.Nodes {
		out = append(out, n.Name)
	}
	return out
}
