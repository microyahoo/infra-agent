package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
)

func TestRenderJSONIsValid(t *testing.T) {
	r, _ := Build(newTestInput())
	out, err := RenderJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if v["cluster_id"] != "fsid-abc" {
		t.Errorf("cluster_id = %v", v["cluster_id"])
	}
	if _, ok := v["evidence"]; !ok {
		t.Error("evidence missing")
	}
}

func TestRenderJSONEscapesRawOutputSafely(t *testing.T) {
	in := newTestInput()
	in.Chain.Add(loop.Evidence{Tool: "x", RawOutput: "output with \"quotes\" and \\ backslash\nand newline"})
	r, _ := Build(in)
	out, err := RenderJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(out)) {
		t.Fatal("invalid json")
	}
	var v map[string]any
	json.Unmarshal([]byte(out), &v)
	ev := v["evidence"].([]any)
	if len(ev) != 3 {
		t.Fatalf("evidence = %d", len(ev))
	}
}

func TestRenderClusterOverview(t *testing.T) {
	rows := []OverviewRow{
		{ClusterID: "stor-01", Health: "HEALTH_WARN", Severity: "high", OSDs: "20/20", Capacity: "62%", Top: "osd.7 坏盘"},
		{ClusterID: "stor-02", Health: "HEALTH_OK", Severity: "none", OSDs: "24/24", Capacity: "48%", Top: "-"},
	}
	out := RenderOverview(rows)
	if !strings.Contains(out, "stor-01") || !strings.Contains(out, "stor-02") {
		t.Error("missing clusters")
	}
	if !strings.Contains(out, "集群") {
		t.Error("missing header")
	}
	if !strings.Contains(out, "62%") {
		t.Error("missing capacity")
	}
}

func TestRenderOverviewEmpty(t *testing.T) {
	out := RenderOverview(nil)
	if !strings.Contains(out, "集群") {
		t.Error("header should still render with no rows")
	}
}
