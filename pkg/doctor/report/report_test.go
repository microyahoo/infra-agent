package report

import (
	"strings"
	"testing"
	"time"

	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
)

func newTestInput() Input {
	chain := loop.NewChain()
	chain.Add(loop.Evidence{
		Tool: "ceph_status", Node: "node-3",
		Command: "ceph status -f json", RawOutput: `{"health":"HEALTH_WARN"}`,
		Summary: "HEALTH_WARN，OSD 20/19", Duration: 2 * time.Second,
	})
	chain.Add(loop.Evidence{
		Tool: "host_io", Node: "node-3",
		Command: "iostat -x 1 3", RawOutput: "sdc 99.8%util",
		Summary: "node-3/sdc 利用率接近 100%", Duration: 4 * time.Second,
	})
	return Input{
		ClusterID:    "fsid-abc",
		Health:       "HEALTH_WARN",
		Capabilities: "host-ceph",
		StartedAt:    time.Date(2026, 10, 23, 15, 32, 0, 0, time.UTC),
		Duration:     42 * time.Second,
		Chain:        chain,
		Conclusion: &loop.Conclusion{
			Severity:     "high",
			Headline:     "osd.7 所在盘 sdc 出现 I/O 错误",
			Impact:       "2/1440 PG 降级，无数据丢失风险",
			EvidenceRefs: []string{"ev-001", "ev-002"},
			RuledOut:     []loop.RuledOut{{Hypothesis: "网络问题", Ref: "ev-001"}},
			Recommendations: []loop.Recommendation{
				{Risk: "low", Action: "观察 15 分钟", Command: "ceph osd ok-to-stop 7"},
			},
			Unknowns: []loop.Unknown{{What: "SMART 状态", Why: "smartctl 不可用", How: "手工执行 smartctl -a /dev/sdc"}},
		},
		Rounds: 4, ToolCalls: 2,
	}
}

func TestBuildBackfillsEvidence(t *testing.T) {
	r, err := Build(newTestInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Evidence) != 2 {
		t.Fatalf("evidence = %d", len(r.Evidence))
	}
	if r.Evidence[0].Command != "ceph status -f json" {
		t.Errorf("command = %q", r.Evidence[0].Command)
	}
	if r.Evidence[1].RawOutput != "sdc 99.8%util" {
		t.Errorf("raw = %q", r.Evidence[1].RawOutput)
	}
	if r.Capabilities != "host-ceph" {
		t.Errorf("capabilities = %q", r.Capabilities)
	}
}

func TestBuildRejectsMissingEvidenceRef(t *testing.T) {
	in := newTestInput()
	in.Conclusion.EvidenceRefs = []string{"ev-001", "ev-404"}
	if _, err := Build(in); err == nil {
		t.Fatal("expected error for missing evidence ref")
	}
}

func TestBuildRejectsMissingRuledOutRef(t *testing.T) {
	in := newTestInput()
	in.Conclusion.RuledOut = []loop.RuledOut{{Hypothesis: "x", Ref: "ev-404"}}
	if _, err := Build(in); err == nil {
		t.Fatal("expected error for missing ruled_out ref")
	}
}

func TestBuildAllowsEmptyRuledOutRef(t *testing.T) {
	in := newTestInput()
	in.Conclusion.RuledOut = []loop.RuledOut{{Hypothesis: "x", Ref: ""}}
	if _, err := Build(in); err != nil {
		t.Errorf("empty ref should not require validation: %v", err)
	}
}

func TestBuildAllowsNilConclusion(t *testing.T) {
	in := newTestInput()
	in.Conclusion = nil
	r, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Severity != "unknown" {
		t.Errorf("severity = %q, want unknown", r.Severity)
	}
	if len(r.Unknowns) == 0 {
		t.Error("nil conclusion should record why there is no conclusion")
	}
}

func TestBuildComputesStats(t *testing.T) {
	r, _ := Build(newTestInput())
	if r.Stats.Rounds != 4 || r.Stats.ToolCalls != 2 {
		t.Errorf("stats = %+v", r.Stats)
	}
	if r.Stats.EvidenceCount != 2 {
		t.Errorf("evidence count = %d", r.Stats.EvidenceCount)
	}
}

func TestBuildSeverityOrdering(t *testing.T) {
	if SeverityRank("critical") <= SeverityRank("high") {
		t.Error("critical should outrank high")
	}
	if SeverityRank("none") >= SeverityRank("low") {
		t.Error("none should rank below low")
	}
}

func TestBuildKeepsNotes(t *testing.T) {
	in := newTestInput()
	in.Notes = []string{"首节点 host key 未校验"}
	r, _ := Build(in)
	if len(r.Notes) != 1 {
		t.Errorf("notes = %v", r.Notes)
	}
	if !strings.Contains(r.Notes[0], "host key") {
		t.Errorf("notes = %v", r.Notes)
	}
}
