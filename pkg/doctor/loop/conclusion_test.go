package loop

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConclusionSchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(ConclusionSchema, &v); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if v["type"] != "object" {
		t.Errorf("type = %v", v["type"])
	}
	props := v["properties"].(map[string]any)
	for _, want := range []string{"severity", "headline", "impact", "evidence_refs", "ruled_out", "recommendations", "unknowns"} {
		if _, ok := props[want]; !ok {
			t.Errorf("schema missing property %q", want)
		}
	}
	required := v["required"].([]any)
	if len(required) != 7 {
		t.Errorf("required has %d entries, want 7", len(required))
	}
}

func TestParseConclusion(t *testing.T) {
	raw := `{
      "severity":"high",
      "headline":"osd.7 所在盘 sdc 出现 I/O 错误",
      "impact":"2/1440 PG 降级，无数据丢失风险",
      "evidence_refs":["ev-003","ev-005"],
      "ruled_out":[{"hypothesis":"网络问题","ref":"ev-001"}],
      "recommendations":[{"risk":"low","action":"观察 15 分钟","command":"ceph osd ok-to-stop 7"}],
      "unknowns":[{"what":"SMART 状态","why":"smartctl 不可用","how":"手工执行 smartctl -a /dev/sdc"}]
    }`
	c, err := ParseConclusion(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.Severity != "high" {
		t.Errorf("severity = %q", c.Severity)
	}
	if len(c.EvidenceRefs) != 2 {
		t.Errorf("refs = %v", c.EvidenceRefs)
	}
	if c.Recommendations[0].Command != "ceph osd ok-to-stop 7" {
		t.Errorf("command = %q", c.Recommendations[0].Command)
	}
}

func TestParseConclusionRejectsUnknownSeverity(t *testing.T) {
	raw := `{"severity":"catastrophic","headline":"x","impact":"y","evidence_refs":[],
             "ruled_out":[],"recommendations":[],"unknowns":[]}`
	if _, err := ParseConclusion(raw); err == nil {
		t.Fatal("expected error for unknown severity")
	}
}

func TestParseConclusionRequiresHeadline(t *testing.T) {
	raw := `{"severity":"high","headline":"","impact":"y","evidence_refs":[],
             "ruled_out":[],"recommendations":[],"unknowns":[]}`
	if _, err := ParseConclusion(raw); err == nil {
		t.Fatal("expected error for empty headline")
	}
}

func TestParseConclusionRejectsBadRecommendationRisk(t *testing.T) {
	raw := `{"severity":"high","headline":"x","impact":"y","evidence_refs":[],
             "ruled_out":[],"recommendations":[{"risk":"whatever","action":"a"}],"unknowns":[]}`
	if _, err := ParseConclusion(raw); err == nil {
		t.Fatal("expected error for invalid risk level")
	}
}

func TestParseConclusionTreatsEmptyUnknownsAsExplicit(t *testing.T) {
	raw := `{"severity":"low","headline":"x","impact":"y","evidence_refs":[],
             "ruled_out":[],"recommendations":[],"unknowns":[]}`
	c, err := ParseConclusion(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.Unknowns == nil {
		t.Error("unknowns should be a non-nil empty slice (explicit 'nothing missing')")
	}
}

func TestConcludeToolName(t *testing.T) {
	if ConcludeToolName != "submit_conclusion" {
		t.Errorf("tool name = %q", ConcludeToolName)
	}
	if !strings.Contains(string(ConclusionToolSpec().Schema), "severity") {
		t.Error("tool schema should describe severity")
	}
}

func TestParseConclusionRejectsOverlongHeadline(t *testing.T) {
	headline := strings.Repeat("长", 300)
	raw := `{"severity":"high","headline":"` + headline + `","impact":"y","evidence_refs":[],
             "ruled_out":[],"recommendations":[],"unknowns":[]}`
	if _, err := ParseConclusion(raw); err == nil {
		t.Fatal("expected error for overlong headline")
	}
}

func TestParseConclusionRejectsEmptyRecommendationAction(t *testing.T) {
	raw := `{"severity":"high","headline":"x","impact":"y","evidence_refs":[],
             "ruled_out":[],"recommendations":[{"risk":"low","action":""}],"unknowns":[]}`
	if _, err := ParseConclusion(raw); err == nil {
		t.Fatal("expected error for empty action")
	}
}

func TestParseConclusionRejectsMalformedJSON(t *testing.T) {
	if _, err := ParseConclusion(`{not json`); err == nil {
		t.Fatal("expected error")
	}
}
