package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ceph "github.com/microyahoo/infra-agent/internal/ceph"
)

const statusJSON = `{
  "fsid": "abc-123",
  "health": {"status": "HEALTH_WARN", "checks": {"PG_DEGRADED": {"severity": "HEALTH_WARN", "summary": {"message": "Degraded data redundancy"}}}},
  "monmap": {"num_mons": 3},
  "osdmap": {"num_osds": 20, "num_up_osds": 19, "num_in_osds": 19},
  "pgmap": {
    "num_pgs": 256,
    "pgs_by_state": [
      {"state_name": "active+clean", "count": 250},
      {"state_name": "active+undersized+degraded", "count": 6}
    ],
    "bytes_total": 10995116277760,
    "bytes_used": 5497558138880,
    "bytes_avail": 5497558138880
  }
}`

func TestCephStatusParsesHealth(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "status", "-f", "json"}, statusJSON)
	tr.on("node-3", []string{"ceph", "health", "detail"}, "HEALTH_WARN 6 pgs degraded")

	tool := NewCephStatus(tr)
	res, raw, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Summary, "HEALTH_WARN") {
		t.Errorf("summary should mention health: %q", res.Summary)
	}
	d := res.Data.(*ceph.Status)
	if d.Health != "HEALTH_WARN" {
		t.Errorf("health = %q", d.Health)
	}
	if d.NumOSDs != 20 || d.NumOSDsUp != 19 {
		t.Errorf("osd counts = %d/%d", d.NumOSDs, d.NumOSDsUp)
	}
	if d.NumPGs != 256 {
		t.Errorf("pgs = %d", d.NumPGs)
	}
	if d.DegradedPGs != 6 {
		t.Errorf("degraded = %d", d.DegradedPGs)
	}
	if raw.Command == "" {
		t.Error("raw command should be recorded")
	}
}

func TestCephStatusHealthyHasNoHints(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "status", "-f", "json"}, `{
      "health":{"status":"HEALTH_OK"},
      "monmap":{"num_mons":3},
      "osdmap":{"num_osds":20,"num_up_osds":20,"num_in_osds":20},
      "pgmap":{"num_pgs":256,"pgs_by_state":[{"state_name":"active+clean","count":256}],
               "bytes_total":100,"bytes_used":50,"bytes_avail":50}}`)
	tr.on("node-3", []string{"ceph", "health", "detail"}, "HEALTH_OK")

	tool := NewCephStatus(tr)
	res, _, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hints) != 0 {
		t.Errorf("healthy cluster should not hint, got %v", res.Hints)
	}
}

func TestCephStatusHintsOnDegraded(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "status", "-f", "json"}, statusJSON)
	tr.on("node-3", []string{"ceph", "health", "detail"}, "detail")

	tool := NewCephStatus(tr)
	res, _, _ := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if len(res.Hints) == 0 {
		t.Fatal("expected hints for degraded cluster")
	}
	// 现在优先引导按告警原文定向排查，而不是固定指向某个工具。
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "定向") && !strings.Contains(joined, "osd_detail") && !strings.Contains(joined, "pg_detail") {
		t.Errorf("expected an actionable hint, got %v", res.Hints)
	}
}

func TestCephStatusUnavailableWithoutCLI(t *testing.T) {
	tr := newFakeRunner()
	tr.caps.CephCLI = false
	tr.caps.Backend = "log-only"

	tool := NewCephStatus(tr)
	_, _, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected unavailable error")
	}
	if !strings.Contains(err.Error(), "log-only") && !strings.Contains(err.Error(), "daemon_log") {
		t.Errorf("error should point to log path, got %v", err)
	}
}

// 大容量低使用率集群（2PB 用 148GB ≈ 0.007%）不应显示成 0.0%。
func TestFormatPercentAvoidsMisleadingZero(t *testing.T) {
	cases := map[float64]string{
		0:      "0%",
		0.0066: "0.0066%",
		0.52:   "0.52%",
		62.5:   "62.5%",
	}
	for in, want := range cases {
		if got := ceph.FormatPercent(in); got != want {
			t.Errorf("FormatPercent(%v) = %q, want %q", in, got, want)
		}
	}
}

// 摘要必须带出告警原文，否则模型只知道 HEALTH_WARN 一个词。
func TestCephStatusSummaryCarriesHealthMessages(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "status", "-f", "json"}, `{
      "fsid":"x","health":{"status":"HEALTH_WARN","checks":{
        "OSD_SLOW_HEARTBEAT":{"severity":"HEALTH_WARN",
          "summary":{"message":"Slow OSD heartbeats on front (longest 1349.426ms)"}}}},
      "monmap":{"num_mons":3},
      "osdmap":{"num_osds":120,"num_up_osds":120,"num_in_osds":120},
      "pgmap":{"num_pgs":3009,"pgs_by_state":[{"state_name":"active+clean","count":3009}],
               "bytes_total":100,"bytes_used":1}}`)

	res, _, err := NewCephStatus(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Summary, "1349.426ms") {
		t.Errorf("summary must carry the measured value: %q", res.Summary)
	}
}
