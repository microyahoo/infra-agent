package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/pkg/doctor/llm"
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
