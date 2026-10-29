package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/internal/access"
)

func TestPGDetailParsesStuck(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "pg", "stat"}, "256 pgs: 250 active+clean; 6 active+undersized+degraded")
	tr.on("node-3", []string{"ceph", "pg", "dump_stuck"}, "2.4a active+undersized+degraded 2026-10-23T15:00:00 3600s 8m\n2.7f active+undersized 2026-10-23T15:00:00 3600s 8m\n")

	res, raw, err := NewPGDetail(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*PGDetailData)
	if d.StuckCount != 2 {
		t.Errorf("stuck = %d", d.StuckCount)
	}
	if len(d.StuckPGs) != 2 || d.StuckPGs[0].ID != "2.4a" {
		t.Errorf("stuck pgs = %+v", d.StuckPGs)
	}
	if !strings.Contains(res.Summary, "2") {
		t.Errorf("summary = %q", res.Summary)
	}
	if raw.Command == "" {
		t.Error("raw missing")
	}
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "osd_detail") {
		t.Errorf("expected osd_detail hint, got %v", res.Hints)
	}
}

func TestPGDetailNoStuck(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "pg", "stat"}, "256 pgs: 256 active+clean")
	tr.on("node-3", []string{"ceph", "pg", "dump_stuck"}, "")

	res, _, err := NewPGDetail(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*PGDetailData)
	if d.StuckCount != 0 {
		t.Errorf("stuck = %d", d.StuckCount)
	}
	if len(res.Hints) != 0 {
		t.Errorf("no stuck should not hint, got %v", res.Hints)
	}
}

func TestPGDetailSurvivesDumpStuckFailure(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "pg", "stat"}, "256 pgs: 250 active+clean")
	tr.fail("node-3", []string{"ceph", "pg", "dump_stuck"}, &access.ToolError{Kind: access.KindTimeout, Message: "超时"})

	res, raw, err := NewPGDetail(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("dump_stuck failure must not fail the tool: %v", err)
	}
	if res.Data.(*PGDetailData).StuckCount != 0 {
		t.Error("expected zero stuck pgs")
	}
	if !strings.Contains(raw.Output, "256 pgs") {
		t.Error("raw should fall back to pg stat output")
	}
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "dump_stuck") {
		t.Errorf("expected hint about dump_stuck failure, got %v", res.Hints)
	}
}

func TestPGDetailStateFilter(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "pg", "stat"}, "256 pgs: 250 active+clean; 6 peering")
	tr.on("node-3", []string{"ceph", "pg", "dump_stuck", "peering"}, "2.4a peering 2026-10-23T15:00:00 3600s 8m\n")

	res, raw, err := NewPGDetail(tr).Execute(context.Background(), []byte(`{"state":"peering"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Data.(*PGDetailData).StuckCount != 1 {
		t.Errorf("stuck = %d", res.Data.(*PGDetailData).StuckCount)
	}
	if !strings.Contains(raw.Command, "peering") {
		t.Errorf("raw command = %q", raw.Command)
	}
}

func TestPGDetailIgnoresNonPGLines(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "pg", "stat"}, "256 pgs: 254 active+clean; 2 stale")
	tr.on("node-3", []string{"ceph", "pg", "dump_stuck"},
		"2026-10-23T15:00:00.000000+0800 mon.node-3 (mon.0) 19 : cluster [WRN] osds down\n"+
			"3.1 stale 2026-10-23T15:00:00 3600s 8m\n")

	res, _, err := NewPGDetail(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*PGDetailData)
	if d.StuckCount != 1 || d.StuckPGs[0].ID != "3.1" || d.StuckPGs[0].State != "stale" {
		t.Errorf("stuck pgs = %+v", d.StuckPGs)
	}
}

func TestLooksLikePGID(t *testing.T) {
	yes := []string{"2.4a", "0.0", "1.ff", "10.3", "1.0"}
	no := []string{"", "2", "2.4a.5", "2.", ".4a", "pg", "2026-10-23T15:00:00", "2.g1", "2.4A-"}
	for _, s := range yes {
		if !looksLikePGID(s) {
			t.Errorf("%q should be a pgid", s)
		}
	}
	for _, s := range no {
		if looksLikePGID(s) {
			t.Errorf("%q should not be a pgid", s)
		}
	}
}
