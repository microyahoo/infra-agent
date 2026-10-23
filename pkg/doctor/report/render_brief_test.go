package report

import (
	"strings"
	"testing"
)

func TestRenderBriefIsShort(t *testing.T) {
	r, _ := Build(newTestInput())
	out := RenderBrief(r)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > 12 {
		t.Errorf("brief too long: %d lines\n%s", len(lines), out)
	}
	if !strings.Contains(out, "osd.7") {
		t.Error("brief should carry the headline")
	}
	if !strings.Contains(strings.ToUpper(out), "HIGH") {
		t.Error("brief should carry severity")
	}
}

func TestRenderBriefOmitsEvidenceDump(t *testing.T) {
	r, _ := Build(newTestInput())
	out := RenderBrief(r)
	if strings.Contains(out, "sdc 99.8%util") {
		t.Error("brief should not dump raw outputs")
	}
}

func TestRenderBriefNoConclusion(t *testing.T) {
	in := newTestInput()
	in.Conclusion = nil
	r, _ := Build(in)
	out := RenderBrief(r)
	if !strings.Contains(out, "未得出结论") {
		t.Errorf("brief = %q", out)
	}
}
