package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

const historicOpsJSON = `{
  "num_ops": 2,
  "ops": [
    {"description":"osd_op(client.4123.0:1 2.4a 2:abc [write 0~4096] v1)","age":32.5,
     "events":[{"event":"queued_for_pg","time":"2026-10-23T15:00:00"},{"event":"waiting_for_ondisk","time":"2026-10-23T15:00:28"}]},
    {"description":"osd_op(client.4123.0:2 2.7f 2:def [write 0~4096] v1)","age":28.1,
     "events":[{"event":"queued_for_pg","time":"2026-10-23T15:00:01"},{"event":"waiting_for_ondisk","time":"2026-10-23T15:00:25"}]}
  ]
}`

func TestSlowOpsParsesHistoricOps(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "tell", "osd.7", "dump_historic_ops"}, historicOpsJSON)
	tr.on("node-3", []string{"ceph", "tell", "osd.8", "dump_historic_ops"}, `{"num_ops":0,"ops":[]}`)
	tr.on("node-3", []string{"ceph", "tell", "osd.9", "dump_historic_ops"}, `{"num_ops":0,"ops":[]}`)

	res, raw, err := NewSlowOps(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*SlowOpsData)
	if d.TotalOps != 2 {
		t.Errorf("total = %d", d.TotalOps)
	}
	if d.PerOSD[7] != 2 {
		t.Errorf("osd.7 slow ops = %d", d.PerOSD[7])
	}
	if len(d.TopOps) != 2 {
		t.Errorf("top ops = %d", len(d.TopOps))
	}
	if d.TopOps[0].Duration < 32 || d.TopOps[0].Duration > 33 {
		t.Errorf("duration = %v", d.TopOps[0].Duration)
	}
	if d.TopOps[0].OSD != 7 {
		t.Errorf("top op osd = %d", d.TopOps[0].OSD)
	}
	if !strings.Contains(strings.Join(d.TopOps[0].Events, ","), "waiting_for_ondisk") {
		t.Errorf("events = %v", d.TopOps[0].Events)
	}
	// 摘要应包含查询规模、总数、分布与最长耗时。
	if !strings.Contains(res.Summary, "3 个 OSD") || !strings.Contains(res.Summary, "2 个 slow op") {
		t.Errorf("summary = %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "osd.7(2)") || !strings.Contains(res.Summary, "32.5s") {
		t.Errorf("summary missing distribution/duration: %q", res.Summary)
	}
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "host_io") {
		t.Errorf("waiting_for_ondisk should hint host_io, got %v", res.Hints)
	}
	// 磁盘方向提示必须先让模型拿到节点与磁盘，再查 host_io。
	if !strings.Contains(joined, "osd_detail(osd_id=7)") {
		t.Errorf("expected osd_detail(osd_id=7) in hints, got %v", res.Hints)
	}
	if raw == nil || raw.Command == "" {
		t.Error("raw missing")
	}
	if raw.Node != "node-3" {
		t.Errorf("raw node = %q", raw.Node)
	}
}

func TestSlowOpsNone(t *testing.T) {
	tr := newFakeRunner()
	for _, id := range []string{"7", "8", "9"} {
		tr.on("node-3", []string{"ceph", "tell", "osd." + id, "dump_historic_ops"}, `{"num_ops":0,"ops":[]}`)
	}
	res, _, err := NewSlowOps(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Data.(*SlowOpsData).TotalOps != 0 {
		t.Errorf("total = %d", res.Data.(*SlowOpsData).TotalOps)
	}
	if len(res.Hints) != 0 {
		t.Errorf("no slow ops should not hint, got %v", res.Hints)
	}
	if !strings.Contains(res.Summary, "共 0 个 slow op") {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestSlowOpsAcceptsExplicitOSDs(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "tell", "osd.9", "dump_historic_ops"}, `{"num_ops":0,"ops":[]}`)

	res, _, err := NewSlowOps(tr).Execute(context.Background(), json.RawMessage(`{"osd_ids":[9]}`))
	if err != nil {
		t.Fatal(err)
	}
	if d := res.Data.(*SlowOpsData); len(d.PerOSD) != 1 || d.PerOSD[9] != 0 {
		t.Errorf("per_osd = %v", d.PerOSD)
	}
}

// 单个 OSD 失败不应影响其他 OSD，且 Raw 应来自成功的那次查询。
func TestSlowOpsContinuesOnPartialFailure(t *testing.T) {
	tr := newFakeRunner()
	tr.fail("node-3", []string{"ceph", "tell", "osd.7", "dump_historic_ops"},
		&access.ToolError{Kind: access.KindTimeout, Message: "命令在时限内未返回"})
	tr.on("node-3", []string{"ceph", "tell", "osd.8", "dump_historic_ops"}, `{"num_ops":0,"ops":[]}`)
	tr.on("node-3", []string{"ceph", "tell", "osd.9", "dump_historic_ops"}, `{"num_ops":0,"ops":[]}`)

	res, raw, err := NewSlowOps(tr).Execute(context.Background(), json.RawMessage(`{"osd_ids":[7,8,9]}`))
	if err != nil {
		t.Fatal(err)
	}
	if raw == nil || !strings.Contains(raw.Command, "osd.9") {
		t.Errorf("raw should come from last successful OSD, got %+v", raw)
	}
	if !strings.Contains(res.Summary, "检查 2 个 OSD") {
		t.Errorf("summary = %q", res.Summary)
	}
}

// 所有 OSD 都失败时才报错。
func TestSlowOpsAllFailed(t *testing.T) {
	tr := newFakeRunner()
	for _, id := range []string{"7", "8"} {
		tr.fail("node-3", []string{"ceph", "tell", "osd." + id, "dump_historic_ops"},
			&access.ToolError{Kind: access.KindTimeout, Message: "命令在时限内未返回"})
	}
	_, _, err := NewSlowOps(tr).Execute(context.Background(), json.RawMessage(`{"osd_ids":[7,8]}`))
	if err == nil {
		t.Fatal("expected error when all OSDs fail")
	}
	var te *access.ToolError
	if !errors.As(err, &te) {
		t.Errorf("expected the underlying ToolError, got %v", err)
	}
}
