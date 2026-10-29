package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestOverviewReportsFindingsAndPlacement(t *testing.T) {
	r := newFakeRunner()
	r.on("node-3", []string{"ceph", "status", "-f", "json"}, `{
      "fsid": "fsid-abc",
      "health": {"status": "HEALTH_WARN"},
      "monmap": {"num_mons": 3},
      "osdmap": {"num_osds": 3, "num_up_osds": 1, "num_in_osds": 3},
      "pgmap": {"num_pgs": 128, "pgs_by_state": [
        {"state_name": "active+clean", "count": 100},
        {"state_name": "active+undersized", "count": 28}
      ]}
    }`)
	r.on("node-3", []string{"ceph", "health", "detail"},
		"HEALTH_WARN 2 osds down\n"+
			"osd.7 (root=default,host=node-3) is down\n"+
			"osd.8 (root=default,host=node-3) is down\n")

	tool := NewOverview(r)
	res, _, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Summary, "HEALTH_WARN") {
		t.Errorf("summary: %q", res.Summary)
	}
	data, ok := res.Data.(*OverviewData)
	if !ok {
		t.Fatalf("data type: %T", res.Data)
	}
	if len(data.Findings) == 0 {
		t.Fatal("expected deterministic findings for two OSDs down on one node")
	}
	if len(data.OSDPlacement) != 2 {
		t.Fatalf("expected placement for osd.7 and osd.8, got %+v", data.OSDPlacement)
	}
	if data.OSDPlacement[0].Node != "node-3" {
		t.Errorf("placement node: %+v", data.OSDPlacement[0])
	}
}

func TestOverviewIncludesPodHealthWhenKubectlExists(t *testing.T) {
	r := newFakeRunner()
	r.caps.Kubectl = true
	r.caps.RookNamespace = "rook-ceph"
	r.on("node-3", []string{"ceph", "status", "-f", "json"}, `{"health":{"status":"HEALTH_OK"},"fsid":"fsid-abc"}`)
	r.on("node-3", []string{"ceph", "health", "detail"}, "")
	r.on("node-3", []string{"ceph", "log", "last", "500"}, "")
	r.on("node-3", []string{"kubectl", "-n", "rook-ceph", "get", "pods", "-o",
		"jsonpath={range .items[*]}{.metadata.name}{\"\\t\"}{.spec.nodeName}{\"\\t\"}{.status.phase}{\"\\t\"}" +
			"{.status.containerStatuses[*].ready}{\"\\t\"}{.status.containerStatuses[*].restartCount}{\"\\t\"}" +
			"{.status.containerStatuses[*].state.waiting.reason}{\"\\n\"}{end}"},
		"rook-ceph-osd-0\tnode-3\tRunning\tfalse\t5\tCrashLoopBackOff\n")

	tool := NewOverview(r)
	res, _, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	data := res.Data.(*OverviewData)
	if data.PodHealth == "" {
		t.Fatal("expected pod health section when kubectl is available")
	}
}
