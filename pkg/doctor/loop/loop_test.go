package loop

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
		return llm.Reply{}, fmt.Errorf("script exhausted after %d calls", m.calls)
	}
	r := m.replies[m.calls]
	m.calls++
	return r, nil
}

// stubTool 返回固定 Result。
type stubTool struct {
	name  string
	res   *tools.Result
	err   error
	calls int
}

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Description() string { return "stub " + s.name }
func (s *stubTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (*tools.Result, *tools.Raw, error) {
	s.calls++
	if s.err != nil {
		return nil, nil, s.err
	}
	return s.res, &tools.Raw{Node: "node-3", Command: "cmd-" + s.name, Output: "raw-output"}, nil
}

func conclusionJSON(severity string, refs []string) string {
	b, _ := json.Marshal(map[string]any{
		"severity":        severity,
		"headline":        "测试结论",
		"impact":          "无影响",
		"evidence_refs":   refs,
		"ruled_out":       []any{},
		"recommendations": []any{map[string]any{"risk": "low", "action": "观察"}},
		"unknowns":        []any{},
	})
	return string(b)
}

func newTestLoop(t *testing.T, model llm.ChatModel, reg *tools.Registry) *SessionRunner {
	t.Helper()
	return NewRunner(RunnerConfig{
		Model:        model,
		Registry:     reg,
		SystemPrompt: "你是测试助手",
		MaxRounds:    5,
		MaxToolCalls: 10,
		MaxPerTool:   2,
	})
}

func TestLoopCompletesOnConclusion(t *testing.T) {
	reg := tools.NewRegistry()
	st := &stubTool{name: "probe", res: &tools.Result{Summary: "发现异常", Hints: []string{"继续查"}}}
	reg.Register(st)

	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "probe", Args: "{}"}}},
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: ConcludeToolName, Args: conclusionJSON("high", []string{"ev-001"})}}},
	}}

	r := newTestLoop(t, model, reg)
	res, err := r.Run(context.Background(), Task{Question: "分析集群"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Conclusion == nil {
		t.Fatal("expected conclusion")
	}
	if res.Conclusion.Severity != "high" {
		t.Errorf("severity = %q", res.Conclusion.Severity)
	}
	if res.Chain.Len() != 1 {
		t.Errorf("evidence = %d", res.Chain.Len())
	}
	if res.Rounds != 2 {
		t.Errorf("rounds = %d", res.Rounds)
	}
}

func TestLoopRejectsFabricatedEvidenceRef(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "probe", res: &tools.Result{Summary: "x"}})

	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: ConcludeToolName, Args: conclusionJSON("low", []string{"ev-999"})}}},
		{ToolCalls: []llm.ToolCall{{ID: "c3", Name: ConcludeToolName, Args: conclusionJSON("low", []string{})}}},
	}}

	r := newTestLoop(t, model, reg)
	res, err := r.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Conclusion == nil {
		t.Fatal("expected conclusion after retry")
	}
	if model.calls != 2 {
		t.Errorf("expected retry on fabricated ref, calls = %d", model.calls)
	}
}

func TestLoopForcesConclusionWhenRoundsExhausted(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "probe", res: &tools.Result{Summary: "x"}})

	var replies []llm.Reply
	for i := 0; i < 5; i++ {
		replies = append(replies, llm.Reply{ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("c%d", i), Name: "probe", Args: fmt.Sprintf(`{"i":%d}`, i)}}})
	}
	replies = append(replies, llm.Reply{ToolCalls: []llm.ToolCall{{ID: "final", Name: ConcludeToolName, Args: conclusionJSON("medium", []string{"ev-001"})}}})

	model := &scriptedModel{replies: replies}
	r := newTestLoop(t, model, reg)
	res, err := r.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Conclusion == nil {
		t.Fatal("expected conclusion after forced wrap-up")
	}
	if !res.BudgetExhausted {
		t.Error("expected BudgetExhausted flag")
	}
}

func TestLoopStopsOnRepeatedIdenticalCall(t *testing.T) {
	reg := tools.NewRegistry()
	st := &stubTool{name: "probe", res: &tools.Result{Summary: "same"}}
	reg.Register(st)

	var replies []llm.Reply
	for i := 0; i < 6; i++ {
		replies = append(replies, llm.Reply{ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("c%d", i), Name: "probe", Args: "{}"}}})
	}
	replies = append(replies, llm.Reply{ToolCalls: []llm.ToolCall{{ID: "f", Name: ConcludeToolName, Args: conclusionJSON("low", nil)}}})

	model := &scriptedModel{replies: replies}
	r := newTestLoop(t, model, reg)
	res, err := r.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if st.calls >= 6 {
		t.Errorf("expected early stop on duplicate calls, calls = %d", st.calls)
	}
	if res.Conclusion == nil {
		t.Fatal("expected conclusion")
	}
}

func TestLoopRecordsToolErrorsAsEvidence(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "probe", err: fmt.Errorf("命令超时")})

	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "probe", Args: "{}"}}},
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: ConcludeToolName, Args: conclusionJSON("low", []string{"ev-001"})}}},
	}}

	r := newTestLoop(t, model, reg)
	res, err := r.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	all := res.Chain.All()
	if len(all) != 1 {
		t.Fatalf("evidence = %d", len(all))
	}
	if all[0].ErrorKind == "" {
		t.Error("tool error should be recorded on evidence")
	}
}

func TestLoopSendsSystemPrompt(t *testing.T) {
	reg := tools.NewRegistry()
	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: ConcludeToolName, Args: conclusionJSON("none", nil)}}},
	}}
	r := newTestLoop(t, model, reg)
	if _, err := r.Run(context.Background(), Task{Question: "q"}); err != nil {
		t.Fatal(err)
	}
	first := model.seenMsgs[0]
	if first[0].Role != llm.RoleSystem {
		t.Fatalf("first message role = %q", first[0].Role)
	}
	if !strings.Contains(first[0].Content, "你是测试助手") {
		t.Error("system prompt not sent")
	}
}

func TestLoopToolCallBudget(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "a", res: &tools.Result{Summary: "x"}})
	reg.Register(&stubTool{name: "b", res: &tools.Result{Summary: "y"}})

	var tcs []llm.ToolCall
	for i := 0; i < 20; i++ {
		name := "a"
		if i%2 == 0 {
			name = "b"
		}
		tcs = append(tcs, llm.ToolCall{ID: fmt.Sprintf("c%d", i), Name: name, Args: fmt.Sprintf(`{"i":%d}`, i)})
	}
	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: tcs},
		{ToolCalls: []llm.ToolCall{{ID: "f", Name: ConcludeToolName, Args: conclusionJSON("low", nil)}}},
	}}

	r := newTestLoop(t, model, reg)
	res, err := r.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Chain.Len() > 10 {
		t.Errorf("tool calls exceeded budget: %d", res.Chain.Len())
	}
}

func TestCompressHistoryShrinksOldToolResults(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "q"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "a", Args: "{}"}}},
		{Role: llm.RoleTool, ToolCallID: "c1", Content: `{"evidence_id":"ev-001","summary":"` + strings.Repeat("长", 500) + `"}`},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c2", Name: "b", Args: "{}"}}},
		{Role: llm.RoleTool, ToolCallID: "c2", Content: `{"evidence_id":"ev-002","summary":"最近的结果"}`},
	}
	out := compressHistory(msgs, 1)
	if len(out) != len(msgs) {
		t.Fatalf("message count changed: %d", len(out))
	}
	if out[0].Content != "sys" || out[1].Content != "q" {
		t.Error("system/user messages must never be compressed")
	}
	if len(out[3].Content) >= len(msgs[3].Content) {
		t.Error("old tool result should be shortened")
	}
	if !strings.Contains(out[3].Content, "ev-001") {
		t.Error("compressed result must keep the evidence id for traceability")
	}
	if out[5].Content != msgs[5].Content {
		t.Error("recent tool result must stay intact")
	}
}

func TestLoopUnknownToolDoesNotCrash(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "probe", res: &tools.Result{Summary: "x"}})
	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "no_such_tool", Args: "{}"}}},
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: ConcludeToolName, Args: conclusionJSON("low", nil)}}},
	}}
	r := newTestLoop(t, model, reg)
	res, err := r.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Chain.Len() != 0 {
		t.Errorf("unknown tool should not create evidence, got %d", res.Chain.Len())
	}
}
