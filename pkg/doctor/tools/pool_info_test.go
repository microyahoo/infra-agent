package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

const poolDFJSON = `{
  "pools": [
    {"name": "rbd", "id": 1, "stats": {"bytes_used": 5497558138880, "max_avail": 5497558138880, "percent_used": 0.5},
     "pg_num": 128, "pg_placement_num": 128, "size": 3, "min_size": 2}
  ]
}`

const poolAutoscaleJSON = `{
  "pools": [
    {"pool_name": "rbd", "pg_num_target": 256, "pg_num": 128, "pg_autoscale_mode": "on"}
  ]
}`

func TestPoolInfoParses(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "df", "detail", "-f", "json"}, poolDFJSON)
	tr.on("node-3", []string{"ceph", "osd", "pool", "autoscale-status", "-f", "json"}, poolAutoscaleJSON)

	res, raw, err := NewPoolInfo(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*PoolInfoData)
	if len(d.Pools) != 1 {
		t.Fatalf("got %d pools", len(d.Pools))
	}
	if d.Pools[0].Name != "rbd" || d.Pools[0].PGNum != 128 {
		t.Errorf("unexpected pool: %+v", d.Pools[0])
	}
	if len(res.Hints) == 0 {
		t.Error("expected hint about pg imbalance")
	}
	if raw.Command == "" {
		t.Error("raw missing")
	}
}

func TestPoolInfoDegradedPGs(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "df", "detail", "-f", "json"}, `{"pools":[
      {"name":"x","stats":{"bytes_used":1,"max_avail":1,"percent_used":0.9},
       "pg_num":64,"size":3,"min_size":2}]}`)
	tr.on("node-3", []string{"ceph", "osd", "pool", "autoscale-status", "-f", "json"}, `{"pools":[]}`)

	res, _, err := NewPoolInfo(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*PoolInfoData)
	if d.Pools[0].UsedPercent < 0.8 {
		t.Errorf("used percent = %v", d.Pools[0].UsedPercent)
	}
}

func TestPoolInfoAutoscaleFailureIgnored(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "df", "detail", "-f", "json"}, poolDFJSON)
	tr.fail("node-3", []string{"ceph", "osd", "pool", "autoscale-status", "-f", "json"},
		&access.ToolError{Kind: access.KindTimeout, Message: "超时"})

	res, _, err := NewPoolInfo(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("autoscale 失败不应导致工具失败: %v", err)
	}
	d := res.Data.(*PoolInfoData)
	if len(d.Pools) != 1 || d.Pools[0].PGNumTarget != 0 {
		t.Fatalf("unexpected pool: %+v", d.Pools)
	}
	if len(res.Hints) != 0 {
		t.Errorf("no target should not hint, got %v", res.Hints)
	}
}

func TestPoolInfoCrossChecksPGNum(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "df", "detail", "-f", "json"}, poolDFJSON)
	// autoscale 报告 pg_num 已经达到目标，而 df 的 pg_num 仍滞后，应以 autoscale 为准。
	tr.on("node-3", []string{"ceph", "osd", "pool", "autoscale-status", "-f", "json"},
		`{"pools":[{"pool_name":"rbd","pg_num_target":128,"pg_num":256,"pg_autoscale_mode":"on"}]}`)

	res, _, err := NewPoolInfo(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	p := res.Data.(*PoolInfoData).Pools[0]
	if p.PGNum != 256 || p.PGNumTarget != 128 {
		t.Errorf("pg_num should come from autoscale: %+v", p)
	}
	for _, h := range res.Hints {
		if strings.Contains(h, "远低于目标") {
			t.Errorf("unexpected hint: %v", res.Hints)
		}
	}
}
