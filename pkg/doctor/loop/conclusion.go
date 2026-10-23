package loop

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/microyahoo/infra-agent/pkg/doctor/llm"
)

// ConcludeToolName 是模型提交最终结论时调用的工具名。
const ConcludeToolName = "submit_conclusion"

// maxHeadlineRunes 是 headline 允许的最大字符数。
// schema 中描述为 100 字，这里放宽到 200 rune，避免中文因字节长度被误判。
const maxHeadlineRunes = 200

// ConclusionSchema 是 submit_conclusion 工具参数的 JSON Schema，
// 会作为工具描述发给模型，约束其结论的字段与取值范围。
var ConclusionSchema = json.RawMessage(`{
  "type":"object",
  "properties":{
    "severity":{"type":"string","enum":["critical","high","medium","low","none"],
                "description":"问题严重程度；集群完全健康时为 none"},
    "headline":{"type":"string","description":"一句话结论，说明根因，不超过 100 字"},
    "impact":{"type":"string","description":"影响范围与数据安全评估"},
    "evidence_refs":{"type":"array","items":{"type":"string"},
                     "description":"支撑结论的证据 ID，如 ev-003；必须是已存在的证据"},
    "ruled_out":{"type":"array","description":"已排除的假设及对应证据",
      "items":{"type":"object","properties":{
        "hypothesis":{"type":"string"},"ref":{"type":"string"}},
        "required":["hypothesis","ref"],"additionalProperties":false}},
    "recommendations":{"type":"array","description":"建议操作，按风险从低到高",
      "items":{"type":"object","properties":{
        "risk":{"type":"string","enum":["low","medium","high"]},
        "action":{"type":"string"},
        "command":{"type":"string","description":"可直接复制执行的命令，可为空"}},
        "required":["risk","action"],"additionalProperties":false}},
    "unknowns":{"type":"array","description":"未能确定的项；全部确定时传空数组",
      "items":{"type":"object","properties":{
        "what":{"type":"string"},"why":{"type":"string"},"how":{"type":"string"}},
        "required":["what","why","how"],"additionalProperties":false}}
  },
  "required":["severity","headline","impact","evidence_refs","ruled_out","recommendations","unknowns"],
  "additionalProperties":false
}`)

// 合法的严重程度取值。
var validSeverities = map[string]bool{
	"critical": true,
	"high":     true,
	"medium":   true,
	"low":      true,
	"none":     true,
}

// 合法的建议风险等级取值。
var validRisks = map[string]bool{
	"low":    true,
	"medium": true,
	"high":   true,
}

// Conclusion 是模型提交的结构化诊断结论。
type Conclusion struct {
	Severity        string           `json:"severity"`
	Headline        string           `json:"headline"`
	Impact          string           `json:"impact"`
	EvidenceRefs    []string         `json:"evidence_refs"`
	RuledOut        []RuledOut       `json:"ruled_out"`
	Recommendations []Recommendation `json:"recommendations"`
	Unknowns        []Unknown        `json:"unknowns"`
}

// RuledOut 描述一个已被排除的假设及其依据的证据 ID。
type RuledOut struct {
	Hypothesis string `json:"hypothesis"`
	Ref        string `json:"ref"`
}

// Recommendation 是一条建议操作，按风险从低到高排序。
type Recommendation struct {
	Risk    string `json:"risk"`
	Action  string `json:"action"`
	Command string `json:"command,omitempty"`
}

// Unknown 描述一项未能确定的排查项及其原因和补查方法。
type Unknown struct {
	What string `json:"what"`
	Why  string `json:"why"`
	How  string `json:"how"`
}

// ParseConclusion 解析并严格校验模型提交的结论 JSON。
// 空数组会被规范化为非 nil 的空切片，以区分“显式声明没有”与“字段缺失”。
func ParseConclusion(raw string) (*Conclusion, error) {
	var c Conclusion
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("结论不是合法的 JSON: %w", err)
	}

	if !validSeverities[c.Severity] {
		return nil, fmt.Errorf("非法的 severity: %q", c.Severity)
	}

	c.Headline = strings.TrimSpace(c.Headline)
	if c.Headline == "" {
		return nil, fmt.Errorf("headline 不能为空")
	}
	if n := utf8.RuneCountInString(c.Headline); n > maxHeadlineRunes {
		return nil, fmt.Errorf("headline 过长: %d 字，上限 %d 字", n, maxHeadlineRunes)
	}

	for i := range c.Recommendations {
		r := &c.Recommendations[i]
		if !validRisks[r.Risk] {
			return nil, fmt.Errorf("recommendations[%d].risk 非法: %q", i, r.Risk)
		}
		r.Action = strings.TrimSpace(r.Action)
		if r.Action == "" {
			return nil, fmt.Errorf("recommendations[%d].action 不能为空", i)
		}
	}

	// 规范化 nil 切片为空切片。
	if c.EvidenceRefs == nil {
		c.EvidenceRefs = []string{}
	}
	if c.RuledOut == nil {
		c.RuledOut = []RuledOut{}
	}
	if c.Recommendations == nil {
		c.Recommendations = []Recommendation{}
	}
	if c.Unknowns == nil {
		c.Unknowns = []Unknown{}
	}

	return &c, nil
}

// ConclusionToolSpec 返回发给模型的 submit_conclusion 工具描述。
func ConclusionToolSpec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: ConcludeToolName,
		Description: "提交最终诊断结论。调查完成时必须在且仅在一次调用中调用本工具，" +
			"不要用自由文本给出结论。evidence_refs 中每一项都必须是本次调查中真实存在的证据 ID（形如 ev-003），" +
			"不得编造；无法确定的事项放入 unknowns，全部确定时传空数组。",
		Schema: ConclusionSchema,
	}
}
