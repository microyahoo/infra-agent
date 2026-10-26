package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
	"github.com/microyahoo/infra-agent/pkg/doctor/llm"
	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
	"github.com/microyahoo/infra-agent/pkg/doctor/report"
	"github.com/microyahoo/infra-agent/pkg/doctor/tools"
)

type fakeCluster struct {
	caps    access.Capabilities
	topo    *access.Topology
	outputs map[string]access.Output
	errs    map[string]error
}

func (f *fakeCluster) Run(_ context.Context, node string, argv []string) (access.Output, error) {
	key := node + "|" + strings.Join(argv, " ")
	if err, ok := f.errs[key]; ok {
		return access.Output{}, err
	}
	if out, ok := f.outputs[key]; ok {
		return out, nil
	}
	return access.Output{}, &access.ToolError{Kind: access.KindFailed, Message: "not stubbed: " + key}
}
func (f *fakeCluster) Capabilities(_ context.Context, _ string) access.Capabilities {
	return f.caps
}
func (f *fakeCluster) Topology(_ context.Context) (*access.Topology, error) { return f.topo, nil }
func (f *fakeCluster) Nodes() []string                                      { return []string{"node-3"} }
func (f *fakeCluster) Close() error                                         { return nil }

type fakeModel struct{ reply llm.Reply }

func (m *fakeModel) Chat(_ context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Reply, error) {
	return m.reply, nil
}

// scriptedModel 按调用顺序依次返回预设的回复，用完后重复最后一条。
type scriptedModel struct {
	replies []llm.Reply
	calls   int
}

func (m *scriptedModel) Chat(_ context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Reply, error) {
	if len(m.replies) == 0 {
		return llm.Reply{}, nil
	}
	i := m.calls
	if i >= len(m.replies) {
		i = len(m.replies) - 1
	}
	m.calls++
	return m.replies[i], nil
}

// mustConclusion 构造一条引用了 refs 的合法 submit_conclusion 参数。
func mustConclusion(t *testing.T, refs []string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"severity": "low", "headline": "一切正常", "impact": "无",
		"evidence_refs": refs, "ruled_out": []any{},
		"recommendations": []any{}, "unknowns": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newFakeCluster() *fakeCluster {
	return &fakeCluster{
		caps: access.Capabilities{
			CephCLI: true, CephVersion: "18.2.4", Backend: "host-ceph",
			LogDirs:   []string{"/var/log/ceph"},
			HostTools: map[string]bool{"iostat": true, "smartctl": true},
		},
		topo: &access.Topology{
			FSID:  "fsid-abc",
			Nodes: []access.TopoNode{{Name: "node-3", Host: "node-3", OSDIDs: []int{7}}},
			OSDs:  []access.OSDPlacement{{ID: 7, Host: "node-3", Device: "sdc", Status: "up"}},
		},
		outputs: map[string]access.Output{
			"node-3|ceph status -f json": {Stdout: `{"fsid":"fsid-abc","health":{"status":"HEALTH_WARN"},"monmap":{"num_mons":3},"osdmap":{"num_osds":1,"num_up_osds":1,"num_in_osds":1},"pgmap":{"num_pgs":10,"pgs_by_state":[{"state_name":"active+clean","count":10}],"bytes_total":100,"bytes_used":50}}`},
			"node-3|ceph health detail":  {Stdout: "HEALTH_WARN"},
		},
		errs: map[string]error{},
	}
}

func conclusionArgs(severity string) string {
	b, _ := json.Marshal(map[string]any{
		"severity": severity, "headline": "一切正常", "impact": "无",
		"evidence_refs": []string{}, "ruled_out": []any{},
		"recommendations": []any{}, "unknowns": []any{},
	})
	return string(b)
}

func TestDoctorBuildsReport(t *testing.T) {
	d := New(Config{
		Cluster: newFakeCluster(),
		Model: &fakeModel{reply: llm.Reply{
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: loop.ConcludeToolName, Args: conclusionArgs("low")}},
		}},
	})
	rep, err := d.Run(context.Background(), Task{Question: "体检"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Headline != "一切正常" {
		t.Errorf("headline = %q", rep.Headline)
	}
	if rep.ClusterID != "fsid-abc" {
		t.Errorf("cluster id = %q", rep.ClusterID)
	}
	if rep.Capabilities != "host-ceph" {
		t.Errorf("capabilities = %q", rep.Capabilities)
	}
	if len(rep.Evidence) == 0 {
		t.Error("expected snapshot evidence")
	}
}

func TestDoctorRendersThreeFormats(t *testing.T) {
	d := New(Config{
		Cluster: newFakeCluster(),
		Model: &fakeModel{reply: llm.Reply{
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: loop.ConcludeToolName, Args: conclusionArgs("high")}},
		}},
	})
	rep, err := d.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	text, err := Render(rep, FormatText)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "一切正常") {
		t.Error("text missing headline")
	}
	brief, err := Render(rep, FormatBrief)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief, "一切正常") {
		t.Error("brief missing headline")
	}
	js, err := Render(rep, FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(js)) {
		t.Error("invalid json")
	}
}

func TestDoctorHandlesUnavailableCLI(t *testing.T) {
	fc := newFakeCluster()
	fc.caps.CephCLI = false
	fc.caps.Backend = "log-only"
	fc.outputs = map[string]access.Output{}
	d := New(Config{
		Cluster: fc,
		Model: &fakeModel{reply: llm.Reply{
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: loop.ConcludeToolName, Args: conclusionArgs("low")}},
		}},
	})
	rep, err := d.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatalf("必须能在没有 ceph CLI 时产出报告: %v", err)
	}
	if rep == nil {
		t.Fatal("expected report")
	}
}

func TestSystemPromptContainsCapabilities(t *testing.T) {
	fc := newFakeCluster()
	snap := Snapshot{
		ClusterID:    "fsid-abc",
		Health:       "HEALTH_WARN",
		Capabilities: fc.caps,
		Topology:     fc.topo,
	}
	p := SystemPrompt(snap, "体检一下")
	for _, want := range []string{"18.2.4", "host-ceph", loop.ConcludeToolName, "不能变更", "node-3"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestRenderRejectsUnknownFormat(t *testing.T) {
	rep, _ := report.Build(report.Input{})
	if _, err := Render(rep, "xml"); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestRenderDefaultsToText(t *testing.T) {
	rep, _ := report.Build(report.Input{})
	out, err := Render(rep, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "集群") {
		t.Errorf("default render should be text: %q", out)
	}
}

func TestDoctorSurvivesSnapshotFailure(t *testing.T) {
	fc := newFakeCluster()
	fc.errs["node-3|ceph status -f json"] = &access.ToolError{Kind: access.KindTimeout, Message: "mon 无响应"}
	d := New(Config{
		Cluster: fc,
		Model: &fakeModel{reply: llm.Reply{
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: loop.ConcludeToolName, Args: conclusionArgs("low")}},
		}},
	})
	rep, err := d.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatalf("快照失败不应让整个诊断失败: %v", err)
	}
	if rep.Health != "unknown" {
		t.Errorf("health = %q, want unknown", rep.Health)
	}
	if len(rep.Notes) == 0 {
		t.Error("expected a note about the snapshot failure")
	}
}

func TestDoctorSnapshotEvidenceComesFirstAndRefsRemap(t *testing.T) {
	fc := newFakeCluster()
	// 模型先调用一次工具（产生 ev-001），再用 submit_conclusion 引用 ev-001
	reg := tools.NewRegistry() // 只是为了确认 registry 构建成功；实际 doctor 内部自建
	_ = reg

	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "ceph_status", Args: "{}"}}},
		{ToolCalls: []llm.ToolCall{{ID: "t2", Name: loop.ConcludeToolName, Args: mustConclusion(t, []string{"ev-001"})}}},
	}}
	d := New(Config{Cluster: fc, Model: model})
	rep, err := d.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	// 快照证据在前，工具证据在后
	if len(rep.Evidence) < 2 {
		t.Fatalf("expected snapshot + tool evidence, got %d", len(rep.Evidence))
	}
	if rep.Evidence[0].Tool != "snapshot:ceph_status" {
		t.Errorf("first evidence should be the snapshot, got %q", rep.Evidence[0].Tool)
	}
	// 结论引用必须在合并后依然有效（Build 会校验）
	if rep.Severity == "unknown" {
		t.Error("conclusion should have survived the merge")
	}
}

func TestMergeChainsRemapsIDs(t *testing.T) {
	snapshot := []loop.Evidence{
		{Tool: "snapshot:ceph_status", Command: "ceph status"},
		{Tool: "snapshot:health_detail", Command: "ceph health detail"},
	}
	investigation := loop.NewChain()
	investigation.MarkRound(1)
	investigation.Add(loop.Evidence{Tool: "ceph_status"}) // 调查链内为 ev-001
	investigation.Add(loop.Evidence{Tool: "osd_tree"})    // 调查链内为 ev-002

	merged, remap := mergeChains(snapshot, investigation)

	all := merged.All()
	if len(all) != 4 {
		t.Fatalf("merged len = %d, want 4", len(all))
	}
	if all[0].Tool != "snapshot:ceph_status" || all[1].Tool != "snapshot:health_detail" {
		t.Errorf("snapshot evidence should come first: %q, %q", all[0].Tool, all[1].Tool)
	}
	if remap["ev-001"] != "ev-003" || remap["ev-002"] != "ev-004" {
		t.Errorf("remap = %v, want ev-001->ev-003, ev-002->ev-004", remap)
	}
	if all[2].Tool != "ceph_status" || all[2].Round != 1 {
		t.Errorf("investigation evidence misplaced: %+v", all[2])
	}

	c := &loop.Conclusion{
		EvidenceRefs: []string{"ev-001"},
		RuledOut:     []loop.RuledOut{{Hypothesis: "h", Ref: "ev-002"}},
	}
	rc := remapConclusion(c, remap)
	if rc.EvidenceRefs[0] != "ev-003" || rc.RuledOut[0].Ref != "ev-004" {
		t.Errorf("remapped conclusion = %+v", rc)
	}
	if c.EvidenceRefs[0] != "ev-001" {
		t.Error("remapConclusion must not mutate the original conclusion")
	}
}

// 会话应复用同一份快照与工具表，追问时不再重复探测。
func TestSessionReusesSnapshotAcrossTurns(t *testing.T) {
	fc := newFakeCluster()
	topoCalls := 0
	counting := &countingCluster{fakeCluster: fc, topoCalls: &topoCalls}

	model := &seqModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: loop.ConcludeToolName, Args: conclusionArgs("low")}}},
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: loop.ConcludeToolName, Args: conclusionArgs("medium")}}},
	}}

	d := New(Config{Cluster: counting, Model: model})
	sess, err := d.OpenSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Ask(context.Background(), "第一次"); err != nil {
		t.Fatal(err)
	}
	second, err := sess.FollowUp(context.Background(), newSessionResult(t), "第二次")
	if err != nil {
		t.Fatal(err)
	}
	if second.Conclusion == nil || second.Conclusion.Severity != "medium" {
		t.Errorf("follow-up conclusion = %+v", second.Conclusion)
	}
	// 快照只做一次：拓扑在 OpenSession 时已确定，追问不应再次探测
	if topoCalls > 1 {
		t.Errorf("topology probed %d times, want 1 (session should reuse the snapshot)", topoCalls)
	}
}

// countingCluster 统计拓扑调用次数。
type countingCluster struct {
	*fakeCluster
	topoCalls *int
}

func (c *countingCluster) Topology(ctx context.Context) (*access.Topology, error) {
	*c.topoCalls++
	return c.fakeCluster.Topology(ctx)
}

// seqModel 按顺序返回预设回复。
type seqModel struct {
	replies []llm.Reply
	i       int
}

func (m *seqModel) Chat(_ context.Context, _ []llm.Message, _ []llm.ToolSpec) (llm.Reply, error) {
	if m.i >= len(m.replies) {
		return llm.Reply{}, fmt.Errorf("replies exhausted")
	}
	r := m.replies[m.i]
	m.i++
	return r, nil
}

// newSessionResult 构造一个空的上轮结果，用于续聊测试。
func newSessionResult(t *testing.T) *loop.Result {
	t.Helper()
	return &loop.Result{Chain: loop.NewChain()}
}
