package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPerfCountersParses(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "tell", "osd.7", "perf", "dump"}, `{
      "osd":{
        "op_w_latency":{"avgcount":100,"sum":2.5,"avg":0.025},
        "op_r_latency":{"avgcount":100,"sum":1.5,"avg":0.015},
        "commit_latency_ms":{"avgcount":100,"sum":300,"avg":3.0}
      }}`)

	res, raw, err := NewPerfCounters(tr).Execute(context.Background(), json.RawMessage(`{"osd_id":7}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*PerfCountersData)
	if d.OSD != 7 {
		t.Errorf("osd = %d", d.OSD)
	}
	if got := d.Avg["op_w_latency"]; got != 0.025 {
		t.Errorf("op_w_latency = %v", got)
	}
	if got := d.Avg["commit_latency_ms"]; got != 3.0 {
		t.Errorf("commit_latency_ms = %v", got)
	}
	if !strings.Contains(res.Summary, "osd.7") {
		t.Errorf("summary = %q", res.Summary)
	}
	// commit 延迟正常时不应给出磁盘方向提示。
	if strings.Contains(strings.Join(res.Hints, " "), "host_io") {
		t.Errorf("normal commit latency should not hint host_io: %v", res.Hints)
	}
	if raw == nil || raw.Command == "" {
		t.Error("raw missing")
	}
}

func TestPerfCountersRequiresOSD(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewPerfCounters(tr).Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing osd_id")
	}
}

// commit_latency_ms > 50 应提示转向 host_io 排查底层磁盘。
func TestPerfCountersHighCommitLatencyHints(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "tell", "osd.7", "perf", "dump"}, `{
      "osd":{"commit_latency_ms":{"avgcount":100,"sum":12000,"avg":120.0}}}`)

	res, _, err := NewPerfCounters(tr).Execute(context.Background(), json.RawMessage(`{"osd_id":7}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Hints, " "), "host_io") {
		t.Errorf("high commit latency should hint host_io, got %v", res.Hints)
	}
}
