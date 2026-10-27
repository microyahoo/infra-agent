package doctor

import (
	"context"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

// fakeCollector 供 profile 测试使用。
type profileFakeCollector struct {
	caps access.Capabilities
	out  map[string]string
}

func (f *profileFakeCollector) Run(_ context.Context, _ string, argv []string) (access.Output, error) {
	if s, ok := f.out[strings.Join(argv, " ")]; ok {
		return access.Output{Stdout: s}, nil
	}
	return access.Output{}, &access.ToolError{Kind: access.KindFailed, Message: "not stubbed"}
}
func (f *profileFakeCollector) Capabilities(context.Context, string) access.Capabilities {
	return f.caps
}
func (f *profileFakeCollector) Topology(context.Context) (*access.Topology, error) {
	return &access.Topology{FSID: "fsid"}, nil
}
func (f *profileFakeCollector) Nodes() []string { return []string{"node-1"} }

// Ceph 集群应自动识别为 ceph profile。
func TestResolveProfileDetectsCeph(t *testing.T) {
	c := &profileFakeCollector{caps: access.Capabilities{CephCLI: true, Backend: "host-ceph"}}
	p, err := ResolveProfile(context.Background(), "auto", c)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "ceph" {
		t.Errorf("profile = %q, want ceph", p.Name())
	}
}

// 非 Ceph 环境应落到 generic，而不是硬编码失败。
func TestResolveProfileFallsBackToGeneric(t *testing.T) {
	c := &profileFakeCollector{caps: access.Capabilities{CephCLI: false, Backend: "log-only"}}
	p, err := ResolveProfile(context.Background(), "auto", c)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "generic" {
		t.Errorf("profile = %q, want generic", p.Name())
	}
}

func TestResolveProfileExplicit(t *testing.T) {
	c := &profileFakeCollector{caps: access.Capabilities{CephCLI: true}}
	p, err := ResolveProfile(context.Background(), "generic", c)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "generic" {
		t.Errorf("explicit profile ignored: %q", p.Name())
	}

	if _, err := ResolveProfile(context.Background(), "cephfs", c); err == nil {
		t.Fatal("unknown profile should error")
	} else if !strings.Contains(err.Error(), "ceph") {
		t.Errorf("error should list available profiles: %v", err)
	}
}

// generic profile 不提供任何 Ceph 专用工具。
func TestGenericProfileHasNoCephTools(t *testing.T) {
	reg := toolsRegistryForTest(GenericProfile{})
	for _, name := range reg.Names() {
		if strings.Contains(name, "ceph") || strings.Contains(name, "osd") || strings.Contains(name, "pg_") {
			t.Errorf("generic profile must not expose ceph tool %q", name)
		}
	}
	if len(reg.Names()) == 0 {
		t.Fatal("generic profile should still expose host/log/explore tools")
	}
}

// generic profile 的提示词明确说"不要套用 Ceph 经验"。
func TestGenericProfileHints(t *testing.T) {
	h := GenericProfile{}.Hints(Snapshot{})
	if !strings.Contains(h, "不要套用") {
		t.Errorf("hints should warn against assuming ceph: %q", h)
	}
	if !strings.Contains(h, "run_command") {
		t.Error("hints should point at the generic escape hatch")
	}
}

// ceph profile 保留原有工具集。
func TestCephProfileKeepsCephTools(t *testing.T) {
	reg := toolsRegistryForTest(CephProfile{})
	if _, ok := reg.Get("ceph_status"); !ok {
		t.Error("ceph profile must keep ceph_status")
	}
	if _, ok := reg.Get("rook_pods"); !ok {
		t.Error("ceph profile must keep rook_pods")
	}
}
