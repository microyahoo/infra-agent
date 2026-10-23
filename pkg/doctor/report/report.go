// Package report 把证据链与结构化结论组装为可读、可信任的最终报告。
//
// 报告中的命令与输出一律由证据链按 ID 回填，模型只负责“引用”；引用不存在的
// 证据 ID 会被视为硬错误，从机制上杜绝编造事实。
package report

import (
	"fmt"
	"time"

	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
)

// Input 是构建报告所需的原始输入。
type Input struct {
	ClusterID    string
	Health       string
	Capabilities string
	StartedAt    time.Time
	Duration     time.Duration
	Chain        *loop.Chain
	Conclusion   *loop.Conclusion
	Rounds       int
	ToolCalls    int
	Notes        []string
}

// Report 是可以直接渲染或序列化的最终报告。
type Report struct {
	ClusterID       string                `json:"cluster_id"`
	Health          string                `json:"health"`
	Capabilities    string                `json:"capabilities,omitempty"`
	Severity        string                `json:"severity"`
	Headline        string                `json:"headline"`
	Impact          string                `json:"impact"`
	StartedAt       time.Time             `json:"started_at"`
	Duration        time.Duration         `json:"duration"`
	Evidence        []EvidenceView        `json:"evidence"`
	RuledOut        []loop.RuledOut       `json:"ruled_out"`
	Recommendations []loop.Recommendation `json:"recommendations"`
	Unknowns        []loop.Unknown        `json:"unknowns"`
	Stats           Stats                 `json:"stats"`
	Notes           []string              `json:"notes,omitempty"`
}

// EvidenceView 是证据链中一条证据在报告里的展示形式，命令与输出均由链回填。
type EvidenceView struct {
	ID        string        `json:"id"`
	Tool      string        `json:"tool"`
	Node      string        `json:"node,omitempty"`
	Command   string        `json:"command,omitempty"`
	Summary   string        `json:"summary,omitempty"`
	RawOutput string        `json:"raw_output,omitempty"`
	Duration  time.Duration `json:"duration"`
	ErrorKind string        `json:"error_kind,omitempty"`
	ErrorMsg  string        `json:"error_message,omitempty"`
}

// Stats 汇总本次诊断的循环统计数据。
type Stats struct {
	Rounds        int `json:"rounds"`
	ToolCalls     int `json:"tool_calls"`
	EvidenceCount int `json:"evidence_count"`
}

// 无结论时的固定文案。
const (
	noConclusionHeadline = "未得出结论"
	noConclusionImpact   = "诊断未产出一致结论，请参考下方证据自行判断"
)

// Build 校验结论中的证据引用并回填真实证据，生成报告。
//
// Conclusion 为 nil 时生成一份“无结论”报告；否则若 EvidenceRefs 或
// 非空的 RuledOut.Ref 引用了不存在的证据 ID，返回错误。
func Build(in Input) (*Report, error) {
	r := &Report{
		ClusterID:       in.ClusterID,
		Health:          in.Health,
		Capabilities:    in.Capabilities,
		StartedAt:       in.StartedAt,
		Duration:        in.Duration,
		Evidence:        make([]EvidenceView, 0),
		RuledOut:        make([]loop.RuledOut, 0),
		Recommendations: make([]loop.Recommendation, 0),
		Unknowns:        make([]loop.Unknown, 0),
		Stats: Stats{
			Rounds:    in.Rounds,
			ToolCalls: in.ToolCalls,
		},
		Notes: in.Notes,
	}

	// 按插入顺序回填证据链中的全部证据。
	if in.Chain != nil {
		for _, e := range in.Chain.All() {
			r.Evidence = append(r.Evidence, EvidenceView{
				ID:        e.ID,
				Tool:      e.Tool,
				Node:      e.Node,
				Command:   e.Command,
				Summary:   e.Summary,
				RawOutput: e.RawOutput,
				Duration:  e.Duration,
				ErrorKind: e.ErrorKind,
				ErrorMsg:  e.ErrorMsg,
			})
		}
	}
	r.Stats.EvidenceCount = len(r.Evidence)

	if in.Conclusion == nil {
		r.Severity = "unknown"
		r.Headline = noConclusionHeadline
		r.Impact = noConclusionImpact
		r.Unknowns = []loop.Unknown{{
			What: "诊断结论",
			Why:  "模型未能在预算内提交有效结论",
			How:  "可查看证据列表手工分析，或提高调查预算后重跑",
		}}
		return r, nil
	}

	c := in.Conclusion

	// 引用校验：任何不存在的证据 ID 都是硬错误。
	var refs []string
	refs = append(refs, c.EvidenceRefs...)
	for i, ro := range c.RuledOut {
		if ro.Ref == "" {
			continue
		}
		if in.Chain == nil {
			return nil, fmt.Errorf("ruled_out[%d] 证据引用 %q 不存在：证据链为空", i, ro.Ref)
		}
		if _, ok := in.Chain.Get(ro.Ref); !ok {
			return nil, fmt.Errorf("ruled_out[%d] 证据引用 %q 不存在", i, ro.Ref)
		}
		refs = append(refs, ro.Ref)
	}
	if len(refs) > 0 {
		if in.Chain == nil {
			return nil, fmt.Errorf("证据引用 %q 不存在：证据链为空", refs[0])
		}
		if err := in.Chain.ValidateRefs(refs); err != nil {
			return nil, err
		}
	}

	r.Severity = c.Severity
	r.Headline = c.Headline
	r.Impact = c.Impact
	r.RuledOut = append(r.RuledOut, c.RuledOut...)
	r.Recommendations = append(r.Recommendations, c.Recommendations...)
	r.Unknowns = append(r.Unknowns, c.Unknowns...)

	return r, nil
}

// SeverityRank 返回严重程度的排序权重，数值越大越严重。
func SeverityRank(s string) int {
	switch s {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		// none 及任何未知取值均视为无异常等级。
		return 0
	}
}
