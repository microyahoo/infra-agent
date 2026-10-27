package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/pkg/doctor/llm"
	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
	"github.com/microyahoo/infra-agent/pkg/doctor/tools"
)

// scriptedModel 按脚本返回预设回复。
type scriptedModel struct {
	replies  []llm.Reply
	calls    int
	seenMsgs [][]llm.Message
}

func (m *scriptedModel) Chat(_ context.Context, msgs []llm.Message, _ []llm.ToolSpec) (llm.Reply, error) {
	m.seenMsgs = append(m.seenMsgs, append([]llm.Message{}, msgs...))
	if m.calls >= len(m.replies) {
		return llm.Reply{}, fmt.Errorf("script exhausted")
	}
	r := m.replies[m.calls]
	m.calls++
	return r, nil
}

type stubTool struct {
	name  string
	res   *tools.Result
	calls int
}

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Description() string { return "stub" }
func (s *stubTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (*tools.Result, *tools.Raw, error) {
	s.calls++
	return s.res, &tools.Raw{Node: "node-3", Command: "cmd"}, nil
}

func newAgentForTest(model llm.ChatModel, reg *tools.Registry) *Agent {
	return NewAgent(AgentConfig{
		Model: model, Registry: reg, SystemPrompt: "test",
		MaxRounds: 5, MaxCallsPerRound: 2, MaxCallsPerTurn: 10,
	})
}

// 核心行为：模型可以直接回答，不需要调用任何工具。
// 旧设计没有这条路径，导致"能不能更准确一点"这类反馈也被迫再查一遍。
func TestAgentCanAnswerWithoutTools(t *testing.T) {
	st := &stubTool{name: "probe", res: &tools.Result{Summary: "x"}}
	reg := tools.NewRegistry()
	reg.Register(st)

	model := &scriptedModel{replies: []llm.Reply{
		{Content: "这是直接回答，没有查任何东西。"},
	}}
	a := newAgentForTest(model, reg)

	reply, err := a.Send(context.Background(), "你觉得呢")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "直接回答") {
		t.Errorf("reply = %q", reply)
	}
	if st.calls != 0 {
		t.Errorf("model answered directly; no tool should run, got %d", st.calls)
	}
	if a.Evidence().Len() != 0 {
		t.Errorf("evidence = %d", a.Evidence().Len())
	}
}

// 需要查证时，模型调工具，拿到结果后作答。
func TestAgentInvestigatesThenAnswers(t *testing.T) {
	st := &stubTool{name: "probe", res: &tools.Result{
		Summary: "osd.13 心跳慢", Hints: []string{"查 host_check"},
	}}
	reg := tools.NewRegistry()
	reg.Register(st)

	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "probe", Args: "{}"}}},
		{Content: "osd.13 心跳慢，可能是节点负载。"},
	}}
	a := newAgentForTest(model, reg)

	reply, err := a.Send(context.Background(), "集群为啥 warn")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "osd.13") {
		t.Errorf("reply = %q", reply)
	}
	if st.calls != 1 {
		t.Errorf("tool calls = %d, want 1", st.calls)
	}
	if a.Evidence().Len() != 1 {
		t.Errorf("evidence = %d", a.Evidence().Len())
	}
}

// 反馈类输入应复用已有证据，而不是重新查。
func TestAgentFeedbackDoesNotReInvestigate(t *testing.T) {
	st := &stubTool{name: "probe", res: &tools.Result{Summary: "osd.13 慢"}}
	reg := tools.NewRegistry()
	reg.Register(st)

	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "probe", Args: "{}"}}},
		{Content: "结论：osd.13 慢。"},
		// 第二轮：用户反馈，模型直接回应，不再查
		{Content: "更准确地说，是 osd.13 所在节点 node03 的负载问题。"},
	}}
	a := newAgentForTest(model, reg)

	if _, err := a.Send(context.Background(), "集群为啥 warn"); err != nil {
		t.Fatal(err)
	}
	before := st.calls

	reply, err := a.Send(context.Background(), "能不能定位更准确一点")
	if err != nil {
		t.Fatal(err)
	}
	if st.calls != before {
		t.Errorf("feedback should not trigger new queries: %d -> %d", before, st.calls)
	}
	if !strings.Contains(reply, "node03") {
		t.Errorf("reply = %q", reply)
	}
	// 第二轮必须能看到第一轮聊过什么：上下文要包含第一次的问答。
	last := model.seenMsgs[len(model.seenMsgs)-1]
	var sb strings.Builder
	for _, m := range last {
		sb.WriteString(m.Content)
	}
	ctxText := sb.String()
	if !strings.Contains(ctxText, "集群为啥 warn") {
		t.Error("follow-up lost the earlier question")
	}
	if !strings.Contains(ctxText, "osd.13 慢") {
		t.Error("follow-up lost the earlier tool result")
	}
}

// 单轮工具调用有上限，超出的丢弃并告知模型。
func TestAgentCapsCallsPerRound(t *testing.T) {
	a1 := &stubTool{name: "a", res: &tools.Result{Summary: "a"}}
	b1 := &stubTool{name: "b", res: &tools.Result{Summary: "b"}}
	c1 := &stubTool{name: "c", res: &tools.Result{Summary: "c"}}
	reg := tools.NewRegistry()
	reg.Register(a1)
	reg.Register(b1)
	reg.Register(c1)

	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{
			{ID: "1", Name: "a", Args: "{}"},
			{ID: "2", Name: "b", Args: "{}"},
			{ID: "3", Name: "c", Args: "{}"},
		}},
		{Content: "done"},
	}}
	a := newAgentForTest(model, reg)
	if _, err := a.Send(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	if c1.calls != 0 {
		t.Errorf("third call should be dropped, c=%d", c1.calls)
	}
	if a.Evidence().Len() != 2 {
		t.Errorf("evidence = %d, want 2", a.Evidence().Len())
	}
}

// 轮数用尽时仍要给出答复，而不是报错。
func TestAgentAnswersWhenRoundsExhausted(t *testing.T) {
	st := &stubTool{name: "probe", res: &tools.Result{Summary: "x"}}
	reg := tools.NewRegistry()
	reg.Register(st)

	var replies []llm.Reply
	for i := 0; i < 5; i++ {
		replies = append(replies, llm.Reply{ToolCalls: []llm.ToolCall{
			{ID: fmt.Sprintf("c%d", i), Name: "probe", Args: fmt.Sprintf(`{"i":%d}`, i)},
		}})
	}
	replies = append(replies, llm.Reply{Content: "只能查到这些，还需要更多信息。"})

	model := &scriptedModel{replies: replies}
	a := newAgentForTest(model, reg)
	reply, err := a.Send(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "只能查到这些") {
		t.Errorf("reply = %q", reply)
	}
}

// 提示词不能要求模型每轮产出结构化报告，也不能强制它调工具。
func TestAgentPromptIsConversational(t *testing.T) {
	s := Snapshot{ClusterID: "x", Health: "HEALTH_WARN",
		HealthDetail: "HEALTH_WARN Slow OSD heartbeats (longest 1349ms)"}
	p := AgentPrompt(s)

	for _, banned := range []string{"submit_conclusion", "## 结论", "## 建议操作", "evidence_refs"} {
		if strings.Contains(p, banned) {
			t.Errorf("prompt still carries report-template residue %q", banned)
		}
	}
	for _, want := range []struct{ key, why string }{
		{"直接回答", "允许不调工具直接回答"},
		{"不要重新全面检查", "反馈类输入不应触发重新调查"},
		{"只读", "只读约束"},
		{"1349ms", "症状出现在提示词里"},
	} {
		if !strings.Contains(p, want.key) {
			t.Errorf("prompt missing %q (%s)", want.key, want.why)
		}
	}
}

// 健康但用户可能报告过异常时，最近的告警历史要进提示词。
func TestAgentPromptCarriesRecentHealthEvents(t *testing.T) {
	s := Snapshot{
		ClusterID: "x", Health: "HEALTH_OK",
		RecentHealthEvents: "2026-10-27 05:20:11 osd.13 is experiencing slow heartbeats",
	}
	p := AgentPrompt(s)
	if !strings.Contains(p, "osd.13") {
		t.Error("recent health events should reach the prompt")
	}
}

// 快照证据应预置进链，并出现在提示词里 —— 否则模型为了拿到 evidence_id
// 会把 ceph_status / ceph_health 再跑一遍，用户看到的就是"没啥变化"。
func TestSnapshotEvidenceIsSeededAndAdvertised(t *testing.T) {
	// 真实路径里 CollectSnapshot 会给它们编号。
	seed := []loop.Evidence{
		{ID: "ev-001", Tool: "snapshot:ceph_status", Command: "ceph status -f json", Summary: "HEALTH_WARN"},
		{ID: "ev-002", Tool: "snapshot:ceph_health", Command: "ceph health detail", Summary: "Slow OSD heartbeats"},
	}
	snap := Snapshot{
		ClusterID: "x", Health: "HEALTH_WARN",
		HealthDetail: "HEALTH_WARN Slow OSD heartbeats (longest 1349ms)",
		Evidence:     seed,
	}
	p := AgentPrompt(snap)
	if !strings.Contains(p, "ev-001") || !strings.Contains(p, "ev-002") {
		t.Errorf("prompt should list seeded evidence ids:\n%s", p)
	}
	if !strings.Contains(p, "不要重复执行") {
		t.Error("prompt should tell the model not to re-run them")
	}

	st := &stubTool{name: "ceph_status", res: &tools.Result{Summary: "x"}}
	reg := tools.NewRegistry()
	reg.Register(st)
	model := &scriptedModel{replies: []llm.Reply{{Content: "直接回答"}}}
	a := NewAgent(AgentConfig{Model: model, Registry: reg, SystemPrompt: p, SeedEvidence: seed})

	if a.Evidence().Len() != 2 {
		t.Errorf("seed evidence not registered: %d", a.Evidence().Len())
	}
	// 第一轮就能看到已采集的证据
	if _, err := a.Send(context.Background(), "为啥 warn"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.seenMsgs[0][0].Content, "ev-001") {
		t.Error("model should see seeded evidence ids in its system prompt")
	}
}

// 相同工具 + 相同参数已执行过时，不重复执行，直接把已有结果告诉模型。
func TestAgentSkipsDuplicateCalls(t *testing.T) {
	st := &stubTool{name: "probe", res: &tools.Result{Summary: "osd.13 慢"}}
	reg := tools.NewRegistry()
	reg.Register(st)

	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "probe", Args: `{"x":1}`}}},
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: "probe", Args: `{"x":1}`}}}, // 相同参数
		{Content: "结论"},
	}}
	a := newAgentForTest(model, reg)
	if _, err := a.Send(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	if st.calls != 1 {
		t.Errorf("duplicate call should be skipped, actual executions = %d", st.calls)
	}
	if a.Evidence().Len() != 1 {
		t.Errorf("evidence = %d, want 1", a.Evidence().Len())
	}
}

// 默认每轮只允许一个工具调用：模型必须看到上一次结果才能决定下一步。
// 允许多个并行调用时，它会在同一轮把想查的都发出去，等于没有"看结果再决定"。
func TestAgentDefaultOneCallPerRound(t *testing.T) {
	a := NewAgent(AgentConfig{})
	if a.maxCallsPerRound != 1 {
		t.Errorf("default maxCallsPerRound = %d, want 1", a.maxCallsPerRound)
	}
}

// 同一轮发出多个调用时，只有第一个执行，其余被丢弃并告知模型。
func TestAgentDropsExtraCallsInSameRound(t *testing.T) {
	first := &stubTool{name: "a", res: &tools.Result{Summary: "a"}}
	second := &stubTool{name: "b", res: &tools.Result{Summary: "b"}}
	third := &stubTool{name: "c", res: &tools.Result{Summary: "c"}}
	reg := tools.NewRegistry()
	reg.Register(first)
	reg.Register(second)
	reg.Register(third)

	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{
			{ID: "1", Name: "a", Args: "{}"},
			{ID: "2", Name: "b", Args: "{}"},
			{ID: "3", Name: "c", Args: "{}"},
		}},
		{Content: "done"},
	}}
	a := NewAgent(AgentConfig{Model: model, Registry: reg, SystemPrompt: "s", MaxRounds: 3, MaxCallsPerTurn: 10})

	if _, err := a.Send(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	if first.calls != 1 {
		t.Errorf("first should run once, got %d", first.calls)
	}
	if second.calls != 0 || third.calls != 0 {
		t.Errorf("extra calls must not run: b=%d c=%d", second.calls, third.calls)
	}
	if a.Evidence().Len() != 1 {
		t.Errorf("evidence = %d, want 1", a.Evidence().Len())
	}
}

// 会话第一条回答不调用工具：先用手上已有的信息作答。
// 这对应"人被问到问题时先看已知信息，而不是先跑命令"。
func TestFirstTurnUsesNoTools(t *testing.T) {
	st := &stubTool{name: "probe", res: &tools.Result{Summary: "x"}}
	reg := tools.NewRegistry()
	reg.Register(st)

	var specCounts []int
	model := &countingModel{reply: llm.Reply{Content: "基于已知信息：node05 有 OSD 在 CrashLoop。"}, specs: &specCounts}
	a := newAgentForTest(model, reg)

	reply, err := a.Send(context.Background(), "集群为啥 warn")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "node05") {
		t.Errorf("reply = %q", reply)
	}
	if len(specCounts) == 0 {
		t.Fatal("model not called")
	}
	if specCounts[0] != 0 {
		t.Errorf("first turn should receive no tools, got %d", specCounts[0])
	}
	if st.calls != 0 {
		t.Errorf("no tool should run on the first turn, got %d", st.calls)
	}
}

// 后续轮次工具恢复可用。
func TestLaterTurnsGetTools(t *testing.T) {
	st := &stubTool{name: "probe", res: &tools.Result{Summary: "osd.44 CrashLoop"}}
	reg := tools.NewRegistry()
	reg.Register(st)

	var specCounts []int
	model := &countingModel{
		script: []llm.Reply{
			{Content: "初步看是 node05 的问题，要看 OSD 日志吗？"},
			{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "probe", Args: "{}"}}},
			{Content: "osd.44 在 CrashLoop。"},
		},
		specs: &specCounts,
	}
	a := newAgentForTest(model, reg)

	if _, err := a.Send(context.Background(), "集群为啥 warn"); err != nil {
		t.Fatal(err)
	}
	if specCounts[0] != 0 {
		t.Errorf("first turn specs = %d, want 0", specCounts[0])
	}

	if _, err := a.Send(context.Background(), "查一下 OSD 日志"); err != nil {
		t.Fatal(err)
	}
	if len(specCounts) < 2 || specCounts[1] == 0 {
		t.Errorf("later turns should receive tools, got %v", specCounts)
	}
	if st.calls != 1 {
		t.Errorf("tool should run on the follow-up, got %d", st.calls)
	}
}

// countingModel 记录每次调用拿到的工具数量。
type countingModel struct {
	script []llm.Reply
	reply  llm.Reply
	specs  *[]int
	i      int
}

func (m *countingModel) Chat(_ context.Context, _ []llm.Message, specs []llm.ToolSpec) (llm.Reply, error) {
	*m.specs = append(*m.specs, len(specs))
	if len(m.script) > 0 {
		if m.i >= len(m.script) {
			return llm.Reply{}, fmt.Errorf("script exhausted")
		}
		r := m.script[m.i]
		m.i++
		return r, nil
	}
	return m.reply, nil
}
