package doctor

import (
	"context"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/internal/access"
)

// fakeCollector 用预置响应模拟集群。
type fakeCollector struct {
	outputs map[string]access.Output
	caps    access.Capabilities
	topo    *access.Topology
}

func (f *fakeCollector) Run(_ context.Context, _ string, argv []string) (access.Output, error) {
	key := strings.Join(argv, " ")
	if out, ok := f.outputs[key]; ok {
		return out, nil
	}
	return access.Output{}, &access.ToolError{Kind: access.KindFailed, Message: "not stubbed: " + key}
}

func (f *fakeCollector) Capabilities(context.Context, string) access.Capabilities { return f.caps }
func (f *fakeCollector) Topology(context.Context) (*access.Topology, error)       { return f.topo, nil }
func (f *fakeCollector) Nodes() []string                                          { return []string{"node-1"} }

func newFakeCollector() *fakeCollector {
	return &fakeCollector{
		caps: access.Capabilities{
			CephCLI: true, Backend: "host-ceph", CephVersion: "16.2.14",
			LogDirs: []string{"/var/lib/rook/rook-ceph/log"},
		},
		topo: &access.Topology{
			FSID:  "fsid-1",
			Nodes: []access.TopoNode{{Name: "node-1"}},
			OSDs:  []access.OSDPlacement{{ID: 1, Host: "node-1", Device: "sda"}},
		},
		outputs: map[string]access.Output{
			"ceph status -f json": {Stdout: `{"fsid":"fsid-1","health":{"status":"HEALTH_WARN"},
              "monmap":{"num_mons":3},"osdmap":{"num_osds":120,"num_up_osds":120,"num_in_osds":120},
              "pgmap":{"num_pgs":3009,"pgs_by_state":[{"state_name":"active+clean","count":3009}],
                       "bytes_total":100,"bytes_used":1}}`},
			"ceph health detail": {Stdout: "HEALTH_WARN Slow OSD heartbeats on front (longest 1349.426ms)"},
			"ceph log last 500":  {Stdout: "2026-10-27 05:20:11 osd.13 is experiencing slow heartbeats"},
		},
	}
}

// 采集必须产出带编号的证据，否则模型只能重跑命令来换 ID。
func TestCollectSnapshotProducesNumberedEvidence(t *testing.T) {
	snap, err := CollectSnapshotFrom(context.Background(), newFakeCollector())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Evidence) < 2 {
		t.Fatalf("evidence = %d, want at least status + health", len(snap.Evidence))
	}
	if snap.Evidence[0].ID != "ev-001" || snap.Evidence[1].ID != "ev-002" {
		t.Errorf("ids = %q/%q", snap.Evidence[0].ID, snap.Evidence[1].ID)
	}
	// 提示词里要能看到这些编号
	p := AgentPrompt(snap)
	for _, id := range []string{"ev-001", "ev-002"} {
		if !strings.Contains(p, id) {
			t.Errorf("prompt missing seeded evidence %s", id)
		}
	}
	// 告警明细要在提示词里
	if !strings.Contains(p, "1349.426ms") {
		t.Error("health detail missing from prompt")
	}
}

// 健康但用户可能报告过异常：日志里的历史告警要进提示词。
func TestCollectSnapshotFindsHistoricalWarnings(t *testing.T) {
	fc := newFakeCollector()
	fc.outputs["ceph status -f json"] = access.Output{Stdout: `{"fsid":"fsid-1","health":{"status":"HEALTH_OK"},
      "monmap":{"num_mons":3},"osdmap":{"num_osds":1,"num_up_osds":1,"num_in_osds":1},
      "pgmap":{"num_pgs":10,"pgs_by_state":[],"bytes_total":100,"bytes_used":1}}`}
	snap, err := CollectSnapshotFrom(context.Background(), fc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snap.RecentHealthEvents, "osd.13") {
		t.Errorf("recent events = %q", snap.RecentHealthEvents)
	}
}
