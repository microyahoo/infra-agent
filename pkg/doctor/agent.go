package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/internal/agent/llm"
	"github.com/microyahoo/infra-agent/internal/ceph/tools"
	"github.com/microyahoo/infra-agent/internal/evidence"
)

// Agent 是一个对话式的集群调查助手。
//
// 与旧设计的根本区别：这里没有"必须提交结构化结论"的约束。
// 模型收到用户的话之后，可以：
//   - 直接回答（已经知道答案、只是反馈、闲聊）
//   - 调用工具查证，再回答
//   - 连续几轮查证后回答
//
// 调查到什么程度、要不要继续查，由模型结合用户的话判断，不由框架预设。
// 证据链仍然记录每次工具调用，供 :evidence 与人工复核。
type Agent struct {
	model   llm.ChatModel
	reg     *tools.Registry
	chain   *evidence.Chain
	msgs    []llm.Message
	onEvent func(string)

	roundsSeen       []string // 已处理过的用户输入，用于判断是否首轮
	maxRounds        int
	maxCallsPerRound int
	maxCallsPerTurn  int
}

// AgentConfig 是 Agent 的构造参数。
type AgentConfig struct {
	Model            llm.ChatModel
	Registry         *tools.Registry
	SystemPrompt     string
	OnEvent          func(string) // 进度回调，仅供显示
	MaxRounds        int          // 单次用户输入内最多几轮模型调用
	MaxCallsPerRound int
	MaxCallsPerTurn  int
	// SeedEvidence 是会话开始前已经采集到的证据（快照）。
	// 预置进证据链后，模型可以直接引用，不必重跑同样的命令来换取 evidence_id。
	SeedEvidence []evidence.Evidence
}

// NewAgent 创建对话式调查助手。
func NewAgent(cfg AgentConfig) *Agent {
	a := &Agent{
		model:            cfg.Model,
		reg:              cfg.Registry,
		chain:            evidence.NewChain(),
		onEvent:          cfg.OnEvent,
		maxRounds:        cfg.MaxRounds,
		maxCallsPerRound: cfg.MaxCallsPerRound,
		maxCallsPerTurn:  cfg.MaxCallsPerTurn,
	}
	if a.maxRounds <= 0 {
		a.maxRounds = 12
	}
	// 默认每轮只允许一个工具调用。
	//
	// 这不是为了限流，而是为了让调查真正是"看一步走一步"：
	// 允许多个并行调用时，模型会在同一轮里一次规划四五个命令，
	// 它根本没有读过任何一个结果，只是把想查的都先发出去。
	// 限定为 1 之后，下一轮必然是在看到上一轮输出之后才做出的决定。
	if a.maxCallsPerRound <= 0 {
		a.maxCallsPerRound = 1
	}
	if a.maxCallsPerTurn <= 0 {
		a.maxCallsPerTurn = 20
	}
	for _, ev := range cfg.SeedEvidence {
		a.chain.Add(ev)
	}
	a.msgs = []llm.Message{{Role: llm.RoleSystem, Content: cfg.SystemPrompt}}
	return a
}

// Send 处理一条用户输入，返回助手的回复文本。
//
// 注意：返回的是给用户看的自然语言，不是结构化结论。
// 需要完整证据时用 Evidence()，需要复盘时用 Transcript()。
func (a *Agent) Send(ctx context.Context, text string) (string, error) {
	if a.model == nil {
		return "", fmt.Errorf("未配置模型")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}

	a.msgs = append(a.msgs, llm.Message{Role: llm.RoleUser, Content: text})
	specs := a.toolSpecs()

	// 会话第一条：先只用已知信息回答。
	//
	// 但"先答"不能变成"答一半就停下等用户催"。因此首轮给它一个
	// need_more_info：如果结论不足以回答问题，模型调用它，系统就在
	// 同一次交互里打开全部诊断工具继续查，而不是把话头抛回给用户。
	answerFirst := len(a.roundsSeen) == 0
	a.roundsSeen = append(a.roundsSeen, text)
	if answerFirst {
		specs = []llm.ToolSpec{needMoreInfoSpec()}
	}

	rounds := 0
	callsThisTurn := 0

	for rounds < a.maxRounds {
		rounds++
		a.event(fmt.Sprintf("第 %d 轮：等待模型返回 …", rounds))

		reply, err := a.model.Chat(ctx, a.msgs, specs)
		if err != nil {
			return "", fmt.Errorf("调用模型失败: %w", err)
		}

		a.event(fmt.Sprintf("第 %d 轮：模型已返回", rounds))

		// 首轮表示"信息不够，要继续查"：同一轮交互内切到完整工具集，
		// 不需要用户再催一次。
		if answerFirst && containsTool(reply.ToolCalls, needMoreToolName) {
			answerFirst = false
			specs = a.toolSpecs()
			a.msgs = append(a.msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: reply.ToolCalls})
			for _, tc := range reply.ToolCalls {
				a.msgs = append(a.msgs, llm.ToolResultMessage(tc.ID,
					"已为你打开全部诊断工具。继续调查，直到能回答用户的问题；"+
						"不需要再询问用户是否继续。"))
			}
			a.event("模型判断需要继续核查，已自动继续")
			continue
		}

		// 没有工具调用 = 模型在回答。这就是本轮的最终答复。
		if len(reply.ToolCalls) == 0 {
			a.msgs = append(a.msgs, llm.Message{Role: llm.RoleAssistant, Content: reply.Content})
			return strings.TrimSpace(reply.Content), nil
		}

		// 单轮并行上限：多出来的丢弃并告知，避免"一口气查所有"。
		calls := reply.ToolCalls
		if len(calls) > a.maxCallsPerRound {
			skipped := len(calls) - a.maxCallsPerRound
			calls = calls[:a.maxCallsPerRound]
			a.event(fmt.Sprintf("本轮请求 %d 个工具，只执行 %d 个", len(reply.ToolCalls), len(calls)))
			a.msgs = append(a.msgs, llm.Message{
				Role: llm.RoleUser,
				Content: fmt.Sprintf("（本轮另有 %d 个工具调用未执行。每次只做一件事："+
					"先看这次返回的结果，再决定下一步要不要查、查什么。）", skipped),
			})
		}

		a.msgs = append(a.msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: calls})

		for _, tc := range calls {
			if callsThisTurn >= a.maxCallsPerTurn {
				a.msgs = append(a.msgs, llm.ToolResultMessage(tc.ID,
					"本轮工具调用已达上限，请基于现有信息回答用户。"))
				continue
			}
			callsThisTurn++
			// 相同工具 + 相同参数已经执行过：不再真的跑一遍，
			// 直接把"已有结果"告诉模型。这是不重复劳动的结构性保证。
			if a.chain.SeenCall(tc.Name, parseArgs(tc.Args)) {
				a.event(fmt.Sprintf("%s 已执行过，跳过", tc.Name))
				a.msgs = append(a.msgs, llm.ToolResultMessage(tc.ID,
					`{"skipped":true,"message":"该调用已执行过，结果在上文。"}`))
				continue
			}
			a.chain.MarkCall(tc.Name, parseArgs(tc.Args))
			content := a.runTool(ctx, tc)
			a.msgs = append(a.msgs, llm.ToolResultMessage(tc.ID, content))
		}
	}

	// 轮数用尽：要求模型基于已有信息作答，不再调工具。
	a.msgs = append(a.msgs, llm.Message{
		Role:    llm.RoleUser,
		Content: "本轮查询次数已用完。请直接基于已经查到的内容回答用户，不要再调用工具。如果信息不足，直说缺什么。",
	})
	reply, err := a.model.Chat(ctx, a.msgs, nil)
	if err != nil {
		return "", fmt.Errorf("调用模型失败: %w", err)
	}
	a.msgs = append(a.msgs, llm.Message{Role: llm.RoleAssistant, Content: reply.Content})
	return strings.TrimSpace(reply.Content), nil
}

// runTool 执行一次工具调用并写入证据链，返回给模型的结果文本。
func (a *Agent) runTool(ctx context.Context, tc llm.ToolCall) string {
	args := parseArgs(tc.Args)
	ev := evidence.Evidence{Tool: tc.Name, Args: args}

	if !a.reg.Has(tc.Name) {
		ev.ErrorKind = "failed"
		ev.ErrorMsg = "不存在该工具"
		a.chain.Add(ev)
		return fmt.Sprintf(`{"error":"工具 %q 不存在。"}`, tc.Name)
	}

	res, raw, err := a.reg.Execute(ctx, tc.Name, json.RawMessage(tc.Args))
	if raw != nil {
		ev.Node, ev.Command, ev.RawOutput = raw.Node, raw.Command, raw.Output
		ev.Duration, ev.ExitCode = raw.Duration, raw.ExitCode
	}
	if err != nil {
		ev.ErrorKind, ev.ErrorMsg = classifyToolError(err)
		if ev.ErrorMsg == "" {
			ev.ErrorMsg = err.Error()
		}
		stored := a.chain.Add(ev)
		a.event(fmt.Sprintf("%s 失败：%s", tc.Name, truncateForLog(ev.ErrorMsg)))
		return fmt.Sprintf(`{"evidence_id":%q,"error_kind":%q,"message":%q}`,
			stored.ID, stored.ErrorKind, stored.ErrorMsg)
	}

	if res != nil {
		ev.Summary = res.Summary
	}
	stored := a.chain.Add(ev)
	a.event(fmt.Sprintf("%s 完成（%s）", tc.Name, stored.ID))

	payload := map[string]any{"evidence_id": stored.ID, "summary": stored.Summary}
	if res != nil {
		if res.Data != nil {
			payload["data"] = res.Data
		}
		if len(res.Hints) > 0 {
			payload["facts"] = res.Hints
		}
	}
	out := mustJSON(payload)
	if len(out) > maxToolResultChars {
		out, _ = truncateUTF8(out, maxToolResultChars)
	}
	return out
}

// Evidence 返回当前证据链。
func (a *Agent) Evidence() *evidence.Chain { return a.chain }

// ToolCalls 返回已执行的工具调用总数。
func (a *Agent) ToolCalls() int { return a.chain.Len() }

// toolSpecs 构造发给模型的工具列表。没有强制性的 submit_conclusion。
func (a *Agent) toolSpecs() []llm.ToolSpec {
	if a.reg == nil {
		return nil
	}
	specs := make([]llm.ToolSpec, 0, len(a.reg.Specs()))
	for _, s := range a.reg.Specs() {
		specs = append(specs, llm.ToolSpec{Name: s.Name, Description: s.Description, Schema: s.Schema})
	}
	return specs
}

func (a *Agent) event(msg string) {
	if a.onEvent != nil {
		a.onEvent(msg)
	}
}

// classifyToolError 把工具错误映射为语义化类型。
func classifyToolError(err error) (kind, msg string) {
	msg = err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "timeout") || strings.Contains(low, "超时"):
		kind = "timeout"
	case strings.Contains(low, "unreachable") || strings.Contains(low, "不可达"):
		kind = "unreachable"
	case strings.Contains(low, "unavailable") || strings.Contains(low, "不可用"):
		kind = "unavailable"
	default:
		kind = "failed"
	}
	return kind, msg
}

func truncateForLog(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= 80 {
		return s
	}
	return string(r[:80]) + "…"
}

// needMoreToolName 是首轮用的"需要继续调查"信号。
const needMoreToolName = "need_more_info"

// needMoreInfoSpec 只在首轮提供。
//
// 它存在的目的，是把"要不要继续查"从用户手里拿回来：模型自己判断，
// 系统自动放行工具，用户不必再回一句"继续"。
func needMoreInfoSpec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: needMoreToolName,
		Description: "当已有信息不足以回答用户的问题、需要继续查询集群时调用它。" +
			"系统会立即为你打开全部诊断工具，你会接着调查。" +
			"不要用它来征求用户同意——需要查就直接调用；只有确实需要用户澄清需求时才用文字提问。",
		Schema: json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string","description":"还需要查什么、为什么"}}}`),
	}
}

// containsTool 判断这批工具调用里是否包含指定工具。
func containsTool(calls []llm.ToolCall, name string) bool {
	for _, c := range calls {
		if c.Name == name {
			return true
		}
	}
	return false
}

// maxToolResultChars 限制单条工具结果进入上下文的长度。
const maxToolResultChars = 6000

// parseArgs 把工具参数解析成 map，解析失败时保留原文便于诊断。
func parseArgs(raw string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]any{"_raw": raw}
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// mustJSON 编码成紧凑 JSON，失败时退化为字符串。
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// truncateUTF8 按 rune 安全截断。
func truncateUTF8(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	r := []rune(s)
	if len(r) <= limit {
		return s, false
	}
	return string(r[:limit]) + "…[已截断]", true
}
