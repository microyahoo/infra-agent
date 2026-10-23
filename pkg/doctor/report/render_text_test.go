package report

import (
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
)

func TestRenderTextContainsAllSections(t *testing.T) {
	r, err := Build(newTestInput())
	if err != nil {
		t.Fatal(err)
	}
	out := RenderText(r)
	for _, section := range []string{"结论", "严重程度", "影响", "判断依据", "已排除", "建议操作", "未能确定"} {
		if !strings.Contains(out, section) {
			t.Errorf("missing section %q", section)
		}
	}
	if !strings.Contains(out, "fsid-abc") {
		t.Error("missing cluster id")
	}
	if !strings.Contains(out, "ceph status -f json") {
		t.Error("missing evidence command")
	}
	if !strings.Contains(out, "ev-001") {
		t.Error("missing evidence id")
	}
	if !strings.Contains(out, "host-ceph") {
		t.Error("missing capabilities")
	}
}

func TestRenderTextNeverEmitsEmptyUnknowns(t *testing.T) {
	in := newTestInput()
	in.Conclusion.Unknowns = []loop.Unknown{}
	r, _ := Build(in)
	out := RenderText(r)
	if !strings.Contains(out, "未能确定") {
		t.Fatal("unknowns section must always exist")
	}
	if !strings.Contains(out, "无") {
		t.Error("empty unknowns should render as 无")
	}
}

func TestRenderTextShowsRecommendationRisk(t *testing.T) {
	r, _ := Build(newTestInput())
	out := RenderText(r)
	if !strings.Contains(out, "低风险") {
		t.Error("expected risk label")
	}
	if !strings.Contains(out, "ceph osd ok-to-stop 7") {
		t.Error("expected command text")
	}
}

func TestRenderTextIncludesNotes(t *testing.T) {
	in := newTestInput()
	in.Notes = []string{"首节点 host key 未校验"}
	r, _ := Build(in)
	out := RenderText(r)
	if !strings.Contains(out, "host key 未校验") {
		t.Error("notes should appear in report")
	}
}

func TestRenderTextEscapesTablePipes(t *testing.T) {
	in := newTestInput()
	in.Chain.Add(loop.Evidence{Tool: "x", Command: "cmd | pipe", Summary: "a | b"})
	r, _ := Build(in)
	out := RenderText(r)
	if !strings.Contains(out, `\|`) {
		t.Error("pipes in table cells should be escaped")
	}
}

func TestRenderTextNoConclusion(t *testing.T) {
	in := newTestInput()
	in.Conclusion = nil
	r, _ := Build(in)
	out := RenderText(r)
	if !strings.Contains(out, "未得出结论") {
		t.Error("missing no-conclusion headline")
	}
	if !strings.Contains(out, "未能确定") {
		t.Error("missing unknowns section")
	}
}
