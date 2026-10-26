package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
	"github.com/microyahoo/infra-agent/pkg/doctor/llm"
	"github.com/microyahoo/infra-agent/pkg/doctor/tools"
)

// 默认预算。均可通过 RunnerConfig 覆盖。
const (
	defaultMaxRounds    = 20
	defaultMaxToolCalls = 60
	defaultMaxPerTool   = 5

	// compressAfterRound 从该轮次起开始压缩历史工具结果。
	compressAfterRound = 3
	// keepRecentToolResults 是压缩时原样保留的最近工具结果条数。
	keepRecentToolResults = 6
	// maxForceAttempts 是强制收尾阶段允许的模型调用次数。
	maxForceAttempts = 3
)

// truncatedContentHint 在回复被 max_tokens 截断（且无工具调用）时发给模型。
// 必须点明是长度问题，否则模型会把截断误判成格式错误并原样重试。
const truncatedContentHint = "你的上一条回复因为超出输出长度上限而被截断了，内容不完整。" +
	"请不要重复同样长度的输出：先用一句话说明当前判断，" +
	"然后直接调用 " + ConcludeToolName + " 提交精简的结论（headline 不超过 100 字）。" +
	"如果还需要更多信息，请只发起必要的工具调用。"

// truncatedArgsHint 在工具调用参数被截断时发给模型。
const truncatedArgsHint = "你的工具调用参数因为超出输出长度上限而被截断了，JSON 不完整。" +
	"请缩小本次调用的参数范围（例如减少 osd_ids 的数量、降低 lines 行数），然后重新调用。"

// 压缩后的工具结果标记。
const compressedMarker = "[已压缩，完整输出见证据链]"

// Task 是一次排查任务。
type Task struct {
	ClusterID string
	Question  string
}

// RunnerConfig 是 SessionRunner 的配置。
type RunnerConfig struct {
	Model        llm.ChatModel
	Registry     *tools.Registry
	SystemPrompt string
	MaxRounds    int // 默认 20
	MaxToolCalls int // 默认 60
	MaxPerTool   int // 默认 5
	OnProgress   func(msg string)
}

// Result 是一次排查的结果。
//
// Messages 保留完整对话（含工具调用与结果），续聊时原样传回，
// 使模型能引用之前查过的事实，而不是从头再来一遍。
type Result struct {
	Conclusion      *Conclusion
	Chain           *Chain
	Rounds          int
	ToolCalls       int
	BudgetExhausted bool
	Usage           llm.Usage

	// TruncatedReplies 统计因 max_tokens 被截断的回复次数。
	// 持续上升说明 max_tokens 偏小，或模型在输出冗长内容。
	TruncatedReplies int

	// Messages 是本次会话的完整消息序列，仅用于续聊（FollowUp）。
	Messages []llm.Message
}

// SessionRunner 驱动模型与工具之间的多轮往返，直到模型提交结论或预算耗尽。
type SessionRunner struct {
	model        llm.ChatModel
	registry     *tools.Registry
	systemPrompt string
	maxRounds    int
	maxToolCalls int
	maxPerTool   int
	onProgress   func(msg string)
}

// NewRunner 创建排查循环，未设置的预算字段使用默认值。
func NewRunner(cfg RunnerConfig) *SessionRunner {
	if cfg.MaxRounds <= 0 {
		cfg.MaxRounds = defaultMaxRounds
	}
	if cfg.MaxToolCalls <= 0 {
		cfg.MaxToolCalls = defaultMaxToolCalls
	}
	if cfg.MaxPerTool <= 0 {
		cfg.MaxPerTool = defaultMaxPerTool
	}
	return &SessionRunner{
		model:        cfg.Model,
		registry:     cfg.Registry,
		systemPrompt: cfg.SystemPrompt,
		maxRounds:    cfg.MaxRounds,
		maxToolCalls: cfg.MaxToolCalls,
		maxPerTool:   cfg.MaxPerTool,
		onProgress:   cfg.OnProgress,
	}
}

// progress 向外部报告进度。
func (r *SessionRunner) progress(msg string) {
	if r.onProgress != nil {
		r.onProgress(msg)
	}
}

// Run 执行排查任务。
//
// 返回的 Result 在出错时也尽量填充已完成的部分（证据链、轮次、用量），
// 便于调用方在模型故障时仍能展示已收集的证据。
func (r *SessionRunner) Run(ctx context.Context, task Task) (*Result, error) {
	if r.model == nil {
		return nil, fmt.Errorf("排查循环缺少模型")
	}
	// 不修改 r 本身，保证同一个 Runner 可被并发复用。
	reg := r.registry
	if reg == nil {
		reg = tools.NewRegistry()
	}

	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: r.systemPrompt},
		{Role: llm.RoleUser, Content: task.Question},
	}
	return r.runLoop(ctx, reg, msgs, &Result{Chain: NewChain()})
}

// FollowUp 在已有会话上继续追问。
//
// 保留此前的对话与证据链，因此模型能看到上次查到的内容，
// 用户也可以就同一个集群连续追问（"那 osd.92 呢？"、"先看网络"）。
// 预算按新一轮重新计算，避免长会话耗尽后无法再问。
func (r *SessionRunner) FollowUp(ctx context.Context, prev *Result, question string) (*Result, error) {
	if prev == nil {
		return r.Run(ctx, Task{Question: question})
	}
	reg := r.registry
	if reg == nil {
		reg = tools.NewRegistry()
	}

	msgs := append([]llm.Message{}, prev.Messages...)
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: question})

	res := &Result{Chain: prev.Chain}
	if res.Chain == nil {
		res.Chain = NewChain()
	}
	return r.runLoop(ctx, reg, msgs, res)
}

// runLoop 是首轮与续聊共用的调查内核。
func (r *SessionRunner) runLoop(ctx context.Context, reg *tools.Registry, msgs []llm.Message, res *Result) (*Result, error) {
	chain := res.Chain
	specs := r.toolSpecs(reg)

	// 计数：per-tool 次数以证据链为准，另用映射统计 submit_conclusion 之外的尝试。
	perTool := make(map[string]int)

	for round := 1; round <= r.maxRounds; round++ {
		chain.MarkRound(round)
		res.Rounds = round
		r.progress(fmt.Sprintf("第 %d/%d 轮：调用模型", round, r.maxRounds))

		if round > compressAfterRound {
			// 压缩旧工具结果，保留最近若干条原文，同时保留 evidence_id 以便追溯。
			msgs = compressHistory(msgs, keepRecentToolResults)
		}

		reply, usage, err := r.chat(ctx, msgs, specs, res)
		res.Usage = usage
		if err != nil {
			return res, fmt.Errorf("第 %d 轮调用模型失败: %w", round, err)
		}

		// 模型未发起工具调用：要求其使用 submit_conclusion；
		// 若正文恰好是合法结论 JSON，则直接采纳。
		if len(reply.ToolCalls) == 0 {
			// 被 max_tokens 截断：内容不完整，不能当作"格式错误"要求重写，
			// 必须明确告知是长度限制，否则模型会用同样长度再试一次。
			if reply.FinishReason.Truncated() {
				msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: reply.Content})
				msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: truncatedContentHint})
				res.TruncatedReplies++
				r.progress(fmt.Sprintf("模型回复被长度限制截断（第 %d 次），已要求精简输出", res.TruncatedReplies))
				continue
			}
			if c, ok := r.tryPlainConclusion(reply.Content, chain); ok {
				res.Conclusion = c
				msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: reply.Content})
				res.Messages = msgs
				r.progress("模型以文本形式给出了合法结论，已采纳")
				return res, nil
			}
			msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: reply.Content})
			msgs = append(msgs, llm.Message{
				Role: llm.RoleUser,
				Content: "请不要用自由文本回答。调查完成时必须调用 " + ConcludeToolName +
					" 工具提交结构化结论；若仍需信息，请继续调用查询工具。",
			})
			r.progress("模型未调用工具，已要求使用 submit_conclusion")
			continue
		}

		// 工具调用被截断时，arguments 必然是残缺的 JSON，
		// 交给下面的解析逻辑只会得到"参数解析失败"，掩盖真实原因。
		if reply.FinishReason.Truncated() && len(reply.ToolCalls) > 0 {
			res.TruncatedReplies++
			msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: reply.Content})
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: truncatedArgsHint})
			r.progress(fmt.Sprintf("工具调用参数被长度限制截断（第 %d 次），已要求精简", res.TruncatedReplies))
			continue
		}

		// 工具调用消息必须与结果消息配对，先登记 assistant 消息。
		msgs = append(msgs, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   reply.Content,
			ToolCalls: reply.ToolCalls,
		})

		stop := false
		for _, tc := range reply.ToolCalls {
			args, argsErr := parseArgs(tc.Args)

			if tc.Name == ConcludeToolName {
				if argsErr != nil {
					msgs = append(msgs, llm.ToolResultMessage(tc.ID,
						fmt.Sprintf("参数解析失败：%v，请修正后重新调用 %s", argsErr, ConcludeToolName)))
					continue
				}
				c, err := ParseConclusion(tc.Args)
				if err == nil {
					err = validateConclusionRefs(c, chain)
				}
				if err != nil {
					msgs = append(msgs, llm.ToolResultMessage(tc.ID,
						"结论被拒绝："+err.Error()+"。evidence_refs 与 ruled_out.ref 只能引用证据链中真实存在的 ID，请修正后重新调用。"))
					r.progress("结论校验失败：" + err.Error())
					continue
				}
				msgs = append(msgs, llm.ToolResultMessage(tc.ID, "结论已接收。"))
				res.Messages = msgs
				res.Conclusion = c
				chain.MarkCall(ConcludeToolName, args)
				return res, nil
			}

			// 总工具调用预算。
			if chain.Len() >= r.maxToolCalls {
				res.BudgetExhausted = true
				stop = true
				break
			}

			name := tc.Name
			if name == "" {
				msgs = append(msgs, llm.ToolResultMessage(tc.ID, "工具调用缺少名称"))
				continue
			}

			// 单工具调用上限。
			if perTool[name] >= r.maxPerTool {
				msgs = append(msgs, llm.ToolResultMessage(tc.ID,
					fmt.Sprintf("工具 %q 已调用 %d 次，达到单工具上限 %d 次，请换用其他工具或提交结论。",
						name, perTool[name], r.maxPerTool)))
				continue
			}

			if !reg.Has(name) {
				msgs = append(msgs, llm.ToolResultMessage(tc.ID,
					fmt.Sprintf("不存在工具 %q，可用工具及 submit_conclusion 请参考工具列表。", name)))
				continue
			}

			if argsErr != nil {
				msgs = append(msgs, llm.ToolResultMessage(tc.ID,
					fmt.Sprintf("参数不是合法的 JSON 对象：%v", argsErr)))
				continue
			}

			// 重复调用检测：相同工具与参数已执行过，结果必然相同。
			if chain.SeenCall(name, args) {
				msgs = append(msgs, llm.ToolResultMessage(tc.ID,
					"相同参数已调用过该工具，结果不会变化，请勿重复调用；请换用其他查询或直接提交结论。"))
				continue
			}

			// 无进展检测：连续两轮以上没有新增证据。
			if chain.NoProgress() {
				res.BudgetExhausted = true
				stop = true
				break
			}

			perTool[name]++
			res.ToolCalls++
			r.progress(fmt.Sprintf("第 %d 轮：执行工具 %s", round, name))

			ev, content := r.executeTool(ctx, reg, chain, round, name, args, tc.Args)
			msgs = append(msgs, llm.ToolResultMessage(tc.ID, content))

			if ev.ErrorKind != "" {
				r.progress(fmt.Sprintf("证据 %s：%s 失败（%s）", ev.ID, name, ev.ErrorKind))
			} else {
				r.progress(fmt.Sprintf("证据 %s：%s 完成", ev.ID, name))
			}
		}
		if stop {
			break
		}
	}

	// 循环结束仍未得出结论：不是失败，转为强制收尾。
	res.BudgetExhausted = true
	r.progress("预算已耗尽，要求模型基于现有证据给出结论")

	res.Messages = msgs
	c, finalMsgs, err := r.forceConclusion(ctx, msgs, chain, res)
	if finalMsgs != nil {
		res.Messages = finalMsgs
	}
	if err != nil {
		return res, err
	}
	res.Conclusion = c
	return res, nil
}

// toolSpecs 构造发给模型的工具列表。
func (r *SessionRunner) toolSpecs(reg *tools.Registry) []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(reg.Specs())+1)
	for _, s := range reg.Specs() {
		specs = append(specs, llm.ToolSpec{Name: s.Name, Description: s.Description, Schema: s.Schema})
	}
	// submit_conclusion 放在最后，避免模型把普通查询与该工具混淆。
	specs = append(specs, ConclusionToolSpec())
	return specs
}

// chat 调用模型并累加用量。
func (r *SessionRunner) chat(ctx context.Context, msgs []llm.Message, specs []llm.ToolSpec, res *Result) (llm.Reply, llm.Usage, error) {
	reply, err := r.model.Chat(ctx, msgs, specs)
	usage := res.Usage
	usage.PromptTokens += reply.Usage.PromptTokens
	usage.CompletionTokens += reply.Usage.CompletionTokens
	return reply, usage, err
}

// tryPlainConclusion 尝试把模型的自由文本解析为结论。
func (r *SessionRunner) tryPlainConclusion(content string, chain *Chain) (*Conclusion, bool) {
	if strings.TrimSpace(content) == "" {
		return nil, false
	}
	c, err := ParseConclusion(content)
	if err != nil {
		return nil, false
	}
	if err := validateConclusionRefs(c, chain); err != nil {
		return nil, false
	}
	return c, true
}

// validateConclusionRefs 校验结论中引用的全部证据 ID 都真实存在。
func validateConclusionRefs(c *Conclusion, chain *Chain) error {
	if err := chain.ValidateRefs(c.EvidenceRefs); err != nil {
		return err
	}
	for _, ro := range c.RuledOut {
		if ro.Ref == "" {
			continue
		}
		if err := chain.ValidateRefs([]string{ro.Ref}); err != nil {
			return fmt.Errorf("ruled_out 中的假设 %q 引用了不存在的证据：%v", ro.Hypothesis, err)
		}
	}
	return nil
}

// executeTool 执行一次工具调用并把结果写入证据链，返回证据与发给模型的结果文本。
func (r *SessionRunner) executeTool(ctx context.Context, reg *tools.Registry, chain *Chain, round int, name string, args map[string]any, rawArgs string) (Evidence, string) {
	ev := Evidence{Tool: name, Args: args, Round: round, StartedAt: time.Now()}

	result, raw, err := reg.Execute(ctx, name, json.RawMessage(rawArgs))
	ev.Duration = time.Since(ev.StartedAt)

	// 即使出错也尽量保留一手材料。
	if raw != nil {
		ev.Node = raw.Node
		ev.Command = raw.Command
		ev.RawOutput = raw.Output
		ev.ExitCode = raw.ExitCode
		ev.Duration = raw.Duration
		if raw.Stderr != "" && raw.Output == "" {
			ev.RawOutput = raw.Stderr
		}
	}

	if err != nil {
		ev.ErrorKind, ev.ErrorMsg = classifyToolError(err)
		if ev.ErrorMsg == "" {
			ev.ErrorMsg = err.Error()
		}
		stored := chain.Add(ev)
		content := mustJSON(map[string]any{
			"error_kind":  stored.ErrorKind,
			"message":     stored.ErrorMsg,
			"evidence_id": stored.ID,
		})
		return stored, content
	}

	if result != nil {
		ev.Summary = result.Summary
	}
	stored := chain.Add(ev)
	content := mustJSON(map[string]any{
		"evidence_id": stored.ID,
		"summary":     stored.Summary,
	})
	return stored, content
}

// classifyToolError 把工具错误映射为语义化的错误类型。
func classifyToolError(err error) (kind, msg string) {
	var te *access.ToolError
	if errors.As(err, &te) {
		return string(te.Kind), te.Message
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return string(access.KindTimeout), err.Error()
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "deadline exceeded") || strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "timed out") || strings.Contains(lower, "命令超时"):
		return string(access.KindTimeout), err.Error()
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "no route to host") ||
		strings.Contains(lower, "unreachable") || strings.Contains(lower, "handshake failed"):
		return string(access.KindUnreachable), err.Error()
	case strings.Contains(lower, "unavailable") || strings.Contains(lower, "not supported") ||
		strings.Contains(lower, "未启用") || strings.Contains(lower, "不可用"):
		return string(access.KindUnavailable), err.Error()
	}
	return string(access.KindFailed), err.Error()
}

// parseArgs 把工具参数解析为 map，解析失败时返回空 map 与错误。
func parseArgs(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]any{}, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// mustJSON 把值编码成紧凑 JSON，编码失败时退化为字符串形式。
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// forceConclusion 在预算耗尽后强制模型给出结论。
//
// 此时工具调用一律禁止，模型只能调用 submit_conclusion；
// 允许若干次重试以容忍格式错误或非法引用。
func (r *SessionRunner) forceConclusion(ctx context.Context, msgs []llm.Message, chain *Chain, res *Result) (*Conclusion, []llm.Message, error) {
	m := msgs
	m = append(m, llm.Message{
		Role: llm.RoleUser,
		Content: "调查预算已耗尽，禁止再调用任何查询工具。请立即调用 " + ConcludeToolName + " 提交结论，" +
			"内容需包含：①已有证据能确定的问题；②已被排除的假设；③仍缺失的证据及获取方式。" +
			"若现有证据不足以支撑任何确定结论，severity 使用 low，并在 unknowns 中说明原因。",
	})

	specs := []llm.ToolSpec{ConclusionToolSpec()}

	var lastErr error
	for attempt := 1; attempt <= maxForceAttempts; attempt++ {
		r.progress(fmt.Sprintf("强制收尾：第 %d/%d 次尝试", attempt, maxForceAttempts))

		reply, usage, err := r.chat(ctx, m, specs, res)
		res.Usage = usage
		if err != nil {
			lastErr = fmt.Errorf("第 %d 次收尾调用模型失败: %w", attempt, err)
			continue
		}

		if reply.FinishReason.Truncated() {
			// 收尾阶段最容易被截断：这一步要一次性输出整份结论 JSON。
			res.TruncatedReplies++
			m = append(m, llm.Message{Role: llm.RoleAssistant, Content: reply.Content})
			m = append(m, llm.Message{Role: llm.RoleUser, Content: truncatedContentHint})
			lastErr = fmt.Errorf("第 %d 次收尾回复被长度限制截断", attempt)
			r.progress("收尾回复被长度限制截断，已要求精简后重试")
			continue
		}

		if len(reply.ToolCalls) == 0 {
			// 纯文本：能解析就采纳，否则要求重新调用工具。
			if c, err := ParseConclusion(reply.Content); err == nil {
				if verr := validateConclusionRefs(c, chain); verr == nil {
					m = append(m, llm.Message{Role: llm.RoleAssistant, Content: reply.Content})
					return c, m, nil
				} else {
					lastErr = verr
				}
			} else {
				lastErr = err
			}
			m = append(m, llm.Message{Role: llm.RoleAssistant, Content: reply.Content})
			m = append(m, llm.Message{
				Role:    llm.RoleUser,
				Content: "请必须调用 " + ConcludeToolName + " 工具提交结论，不要输出自由文本。",
			})
			continue
		}

		m = append(m, llm.Message{Role: llm.RoleAssistant, Content: reply.Content, ToolCalls: reply.ToolCalls})

		for _, tc := range reply.ToolCalls {
			if tc.Name != ConcludeToolName {
				m = append(m, llm.ToolResultMessage(tc.ID,
					fmt.Sprintf("收尾阶段禁止调用工具 %q，请直接调用 %s。", tc.Name, ConcludeToolName)))
				lastErr = fmt.Errorf("收尾阶段模型尝试调用工具 %q", tc.Name)
				continue
			}
			c, err := ParseConclusion(tc.Args)
			if err == nil {
				err = validateConclusionRefs(c, chain)
			}
			if err != nil {
				m = append(m, llm.ToolResultMessage(tc.ID, "结论被拒绝："+err.Error()+"，请修正后重新提交。"))
				lastErr = err
				continue
			}
			m = append(m, llm.ToolResultMessage(tc.ID, "结论已接收。"))
			return c, m, nil
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("模型未给出结论")
	}
	return nil, m, fmt.Errorf("预算耗尽后仍未能获得有效结论: %w", lastErr)
}

// compressHistory 压缩较早的工具结果以控制上下文增长。
//
// 规则：system/user/assistant 消息一律不动；最近 keepRecent 条 tool 消息原样保留；
// 更早的 tool 消息替换为只含证据 ID 与压缩标记的短文本，保证消息数量与配对关系不变。
func compressHistory(msgs []llm.Message, keepRecent int) []llm.Message {
	if keepRecent < 0 {
		keepRecent = 0
	}

	// 统计 tool 消息总数，确定需要压缩的数量。
	toolTotal := 0
	for _, m := range msgs {
		if m.Role == llm.RoleTool {
			toolTotal++
		}
	}
	toCompress := toolTotal - keepRecent
	if toCompress <= 0 {
		return append([]llm.Message{}, msgs...)
	}

	out := make([]llm.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		if out[i].Role != llm.RoleTool || toCompress == 0 {
			continue
		}
		out[i].Content = compressToolContent(out[i].Content)
		toCompress--
	}
	return out
}

// compressToolContent 把一条工具结果压缩为保留证据 ID 的短文本。
func compressToolContent(content string) string {
	id := extractEvidenceID(content)
	if id == "" {
		return compressedMarker
	}
	return `{"evidence_id":` + mustJSON(id) + `,"note":"` + compressedMarker + `"}`
}

// extractEvidenceID 从工具结果 JSON 中提取 evidence_id。
func extractEvidenceID(content string) string {
	var v struct {
		EvidenceID string `json:"evidence_id"`
	}
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return ""
	}
	return v.EvidenceID
}
