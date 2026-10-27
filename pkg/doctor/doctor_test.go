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

// 发生截断时报告必须留下说明，否则用户看不出结论为何不完整。
func TestReportNotesTruncation(t *testing.T) {
	d := New(Config{
		Cluster: newFakeCluster(),
		Model: &fakeModel{reply: llm.Reply{
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: loop.ConcludeToolName, Args: conclusionArgs("low")}},
		}},
	})
	rep, err := d.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	// 正常路径不应有截断说明
	for _, n := range rep.Notes {
		if strings.Contains(n, "截断") {
			t.Errorf("unexpected truncation note on healthy run: %q", n)
		}
	}
}

// 提示词必须把模型当调查者而不是脚本：要有假设驱动、要区分"没有数据"和"没有问题"。
func TestPromptIsInvestigationProtocol(t *testing.T) {
	fc := newFakeCluster()
	snap := Snapshot{
		ClusterID:    "fsid-abc",
		Health:       "HEALTH_WARN",
		Capabilities: fc.caps,
		Topology:     fc.topo,
	}
	p := SystemPrompt(snap, "集群看着异常")

	checks := map[string]string{
		"必须调 health detail": "ceph_health",
		"假设驱动":              "假设",
		"要区分观测失败":           "没能观测",
		"用工具给的提示":           "next_hints",
		"不能变更":              "不能变更",
	}
	for why, want := range checks {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q (%s)", want, why)
		}
	}
	// 不能再是"跑一遍标准检查"的语气
	if strings.Contains(p, "请现在开始调查：先根据上面的快照判断需要补查哪些信息") {
		t.Error("prompt still uses the old generic phrasing")
	}
}

// 健康状况必须每轮刷新：会话开始时 HEALTH_OK，之后变 WARN，
// 追问时模型必须看到 WARN，而不是沿用旧值。
func TestSessionRefreshesHealthOnFollowUp(t *testing.T) {
	fc := newMutableHealthCluster()
	model := &seqModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: loop.ConcludeToolName, Args: conclusionArgs("none")}}},
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: loop.ConcludeToolName, Args: conclusionArgs("high")}}},
	}}

	d := New(Config{Cluster: fc, Model: model})
	sess, err := d.OpenSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first, err := sess.Ask(context.Background(), "第一次")
	if err != nil {
		t.Fatal(err)
	}
	// 集群状态在这之后恶化
	fc.statusCalls = 0
	fc.warn = true

	second, err := sess.FollowUp(context.Background(), first, "现在呢")
	if err != nil {
		t.Fatal(err)
	}
	if second.Conclusion == nil {
		t.Fatal("expected conclusion")
	}
	if fc.statusCalls == 0 {
		t.Error("follow-up must re-query ceph status")
	}
}

// newMutableHealthCluster 的健康状态可切换，用于验证刷新行为。
func newMutableHealthCluster() *mutableHealthCluster {
	fc := newFakeCluster()
	return &mutableHealthCluster{fakeCluster: fc}
}

type mutableHealthCluster struct {
	*fakeCluster
	warn        bool
	statusCalls int
}

func (m *mutableHealthCluster) Run(ctx context.Context, node string, argv []string) (access.Output, error) {
	if strings.Join(argv, " ") == "ceph status -f json" {
		m.statusCalls++
		health := "HEALTH_OK"
		if m.warn {
			health = "HEALTH_WARN"
		}
		return access.Output{
			Stdout: `{"fsid":"fsid-abc","health":{"status":"` + health + `"},"monmap":{"num_mons":3},"osdmap":{"num_osds":1,"num_up_osds":1,"num_in_osds":1},"pgmap":{"num_pgs":10,"pgs_by_state":[{"state_name":"active+clean","count":10}],"bytes_total":100,"bytes_used":50}}`,
		}, nil
	}
	return m.fakeCluster.Run(ctx, node, argv)
}

// 健康明细必须出现在提示词里。否则模型只知道 HEALTH_WARN，
// 不知道是"Slow OSD heartbeats longest 1349ms"，只能全量跑工具去碰。
func TestPromptCarriesHealthDetail(t *testing.T) {
	snap := Snapshot{
		ClusterID:    "fsid-abc",
		Health:       "HEALTH_WARN",
		Summary:      "mon 3，OSD 120",
		HealthDetail: "HEALTH_WARN Slow OSD heartbeats on front (longest 1349.426ms)\n    osd.13 is experiencing slow heartbeats",
		Capabilities: newFakeCluster().caps,
		Topology:     newFakeCluster().topo,
	}
	p := SystemPrompt(snap, "集群异常")

	if !strings.Contains(p, "1349.426ms") {
		t.Error("prompt must carry the measured value from health detail")
	}
	if !strings.Contains(p, "osd.13") {
		t.Error("prompt must carry the affected OSD from health detail")
	}
	if !strings.Contains(p, "不要再调用 ceph_health") {
		t.Error("prompt should tell the model the detail is already collected")
	}
}

// 健康时不应出现明细区块。
func TestPromptWithoutHealthDetail(t *testing.T) {
	snap := Snapshot{
		ClusterID: "fsid-abc", Health: "HEALTH_OK",
		Capabilities: newFakeCluster().caps,
	}
	p := SystemPrompt(snap, "q")
	if strings.Contains(p, "健康告警明细") {
		t.Error("healthy cluster should not render the health detail block")
	}
}

// 用户说有异常、但快照采样是 HEALTH_OK（间歇性告警）时，
// 提示词必须给出"这是间歇性的 + 从历史日志找证据"的明确指引，
// 否则模型会以为集群没事，然后漫无目的地全量查询。
func TestPromptHandlesTransientWarning(t *testing.T) {
	snap := Snapshot{
		ClusterID: "fsid-abc",
		Health:    "HEALTH_OK",
		RecentHealthEvents: "2026-10-27 05:20:11 osd.13 is experiencing slow heartbeats\n" +
			"2026-10-27 05:20:30 Health check OSD_SLOW_HEARTBEAT cleared",
		Capabilities: newFakeCluster().caps,
	}
	p := SystemPrompt(snap, "帮忙检查一下集群为啥 warn 了")

	if !strings.Contains(p, "间歇性") {
		t.Error("prompt should explain that the warning is transient")
	}
	if !strings.Contains(p, "osd.13") {
		t.Error("prompt should surface the OSD from history")
	}
	if !strings.Contains(p, "不要忽略它们转而做全面体检") {
		t.Error("prompt should steer away from a blanket sweep")
	}
}

// 健康明细必须出现在提示词靠前的位置，而不是被规则和拓扑挤到后面。
func TestPromptPutsSymptomEarly(t *testing.T) {
	snap := Snapshot{
		ClusterID:    "x",
		Health:       "HEALTH_WARN",
		HealthDetail: "HEALTH_WARN Slow OSD heartbeats on front (longest 1349.426ms)",
		Capabilities: newFakeCluster().caps,
		Topology:     newFakeCluster().topo,
	}
	p := SystemPrompt(snap, "为啥 warn 了")

	iSymptom := strings.Index(p, "1349.426ms")
	iRules := strings.Index(p, "## 硬性规则")
	if iSymptom < 0 {
		t.Fatal("symptom missing")
	}
	if iRules >= 0 && iSymptom > iRules {
		t.Errorf("symptom at %d should come before the rules at %d", iSymptom, iRules)
	}
}

// 健康明细即使集群当前 OK 也要采集：慢心跳是间歇性的，
// 只在非健康时采集会错过刚恢复的告警。
func TestSnapshotAlwaysFetchesHealthDetail(t *testing.T) {
	fc := newFakeCluster()
	calls := 0
	counting := &countingHealthCluster{fakeCluster: fc, calls: &calls}

	d := New(Config{Cluster: counting, Model: &fakeModel{
		reply: llm.Reply{ToolCalls: []llm.ToolCall{{ID: "c", Name: loop.ConcludeToolName, Args: conclusionArgs("none")}}},
	}})
	if _, err := d.Run(context.Background(), Task{Question: "q"}); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Error("health detail must be fetched even when the snapshot is HEALTH_OK")
	}
}

type countingHealthCluster struct {
	*fakeCluster
	calls *int
}

func (c *countingHealthCluster) Run(ctx context.Context, node string, argv []string) (access.Output, error) {
	if strings.Join(argv, " ") == "ceph health detail" {
		*c.calls++
	}
	return c.fakeCluster.Run(ctx, node, argv)
}
