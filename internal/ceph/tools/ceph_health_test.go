package tools

import (
	"context"
	"strings"
	"testing"
)

// 真实形态：HEALTH_WARN + checks map，summary 带关键数值，detail 指明具体 OSD。
const healthWarnJSON = `{
  "status": "HEALTH_WARN",
  "checks": {
    "OSD_SLOW_HEARTBEAT": {
      "severity": "HEALTH_WARN",
      "summary": {"message": "Slow OSD heartbeats on back (longest 1237.246ms)"},
      "detail": [
        {"message": "osd.13 is experiencing slow heartbeats"},
        {"message": "osd.46 is experiencing slow heartbeats"}
      ]
    }
  },
  "mutes": []
}`

func TestCephHealthParsesWarn(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "health", "detail", "-f", "json"}, healthWarnJSON)

	res, raw, err := NewCephHealth(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*CephHealthData)
	if d.Status != "HEALTH_WARN" {
		t.Errorf("status = %q", d.Status)
	}
	if len(d.Checks) != 1 {
		t.Fatalf("checks = %d", len(d.Checks))
	}
	c := d.Checks[0]
	if c.Code != "OSD_SLOW_HEARTBEAT" {
		t.Errorf("code = %q", c.Code)
	}
	// 关键：summary 必须带量化信息，模型才知道问题多大
	if !strings.Contains(c.Summary, "1237") {
		t.Errorf("summary should carry the measured value: %q", c.Summary)
	}
	if len(c.Details) != 2 {
		t.Errorf("details = %v", c.Details)
	}
	// 摘要要让模型一眼看到问题
	if !strings.Contains(res.Summary, "1237") {
		t.Errorf("result summary = %q", res.Summary)
	}
	if raw.Command == "" {
		t.Error("raw missing")
	}
}

// 关键行为：心跳慢应指向"网络/负载"，而不是磁盘。
// 否则模型会去查 iostat，浪费轮次在错误方向。
func TestCephHealthHintsPointAwayFromDiskForHeartbeat(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "health", "detail", "-f", "json"}, healthWarnJSON)

	res, _, err := NewCephHealth(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "host_check") {
		t.Errorf("heartbeat hint should suggest host_check: %v", res.Hints)
	}
	if strings.Contains(joined, "smartctl -a") || strings.Contains(joined, "磁盘慢") {
		t.Errorf("heartbeat is not a disk issue, hints must not point at disk: %v", res.Hints)
	}
	// 明细里点名的 OSD 要能直接转成下一步查询
	if !strings.Contains(joined, "osd_id=13") {
		t.Errorf("should offer osd_detail for the named OSD: %v", res.Hints)
	}
}

func TestCephHealthHealthyHasNoHints(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "health", "detail", "-f", "json"}, `{"status":"HEALTH_OK","checks":{},"mutes":[]}`)

	res, _, err := NewCephHealth(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hints) != 0 {
		t.Errorf("healthy cluster should not hint: %v", res.Hints)
	}
	if !strings.Contains(res.Summary, "HEALTH_OK") {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestCephHealthPGDegradedHint(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "health", "detail", "-f", "json"}, `{
      "status":"HEALTH_ERR",
      "checks":{"PG_DEGRADED":{"severity":"HEALTH_ERR",
        "summary":{"message":"Degraded data redundancy: 42/9000 objects degraded"},
        "detail":[]}}}`)

	res, _, err := NewCephHealth(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "pg_detail") {
		t.Errorf("PG degraded should hint pg_detail: %v", res.Hints)
	}
}

func TestCephHealthToleratesStringDetails(t *testing.T) {
	tr := newFakeRunner()
	// 某些版本 detail 是裸字符串而非对象
	tr.on("node-3", []string{"ceph", "health", "detail", "-f", "json"}, `{
      "status":"HEALTH_WARN",
      "checks":{"OSD_DOWN":{"severity":"HEALTH_WARN",
        "summary":{"message":"1 osds down"},
        "detail":["osd.7 is down"]}}}`)

	res, _, err := NewCephHealth(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := res.Data.(*CephHealthData).Checks[0]
	if len(c.Details) != 1 || !strings.Contains(c.Details[0], "osd.7") {
		t.Errorf("string detail not parsed: %v", c.Details)
	}
}

func TestCephHealthWithoutCLI(t *testing.T) {
	tr := newFakeRunner()
	tr.caps.CephCLI = false
	tr.caps.Backend = "log-only"
	_, _, err := NewCephHealth(tr).Execute(context.Background(), nil)
	if err == nil {
		t.Fatal("expected unavailable error")
	}
	if !strings.Contains(err.Error(), "log-only") {
		t.Errorf("error should name the backend: %v", err)
	}
}

func TestExtractOSDID(t *testing.T) {
	cases := map[string]struct {
		id int
		ok bool
	}{
		"osd.13 is experiencing slow heartbeats": {13, true},
		"osd.7 is down":                          {7, true},
		"no osd here":                            {0, false},
		"osd.abc":                                {0, false},
	}
	for in, want := range cases {
		id, ok := extractOSDID(in)
		if ok != want.ok || (ok && id != want.id) {
			t.Errorf("extractOSDID(%q) = %d,%v want %d,%v", in, id, ok, want.id, want.ok)
		}
	}
}
