package doctor

import (
	"strings"
	"testing"
)

// 真实告警：6 个 OSD 全在 node05 上同时 down。
// 这是单节点故障，框架必须自己看出来，而不是让模型逐个查。
const realDetail = `HEALTH_WARN 6 osds down; Slow OSD heartbeats on back (longest 10072.200ms); Slow OSD heartbeats on front (longest 11290.452ms); Reduced data availability: 5 pgs peering; Degraded data redundancy: 1110 pgs undersized; 111 slow ops, oldest one blocked for 1525 sec, daemons [osd.111,osd.39,mon.a] have slow ops.
[WRN] OSD_DOWN: 6 osds down
    osd.44 (root=root-x,host=cloud-hdd05-node05-77876dc2-4900-4823-95c2-64641c9e7443) is down
    osd.55 (root=root-x,host=cloud-hdd05-node05-77876dc2-4900-4823-95c2-64641c9e7443) is down
    osd.85 (root=root-x,host=cloud-hdd05-node05-77876dc2-4900-4823-95c2-64641c9e7443) is down
    osd.95 (root=root-x,host=cloud-hdd05-node05-77876dc2-4900-4823-95c2-64641c9e7443) is down
    osd.105 (root=root-x,host=cloud-hdd05-node05-77876dc2-4900-4823-95c2-64641c9e7443) is down
    osd.113 (root=root-x,host=cloud-hdd05-node05-77876dc2-4900-4823-95c2-64641c9e7443) is down
[WRN] PG_AVAILABILITY: Reduced data availability: 5 pgs peering
    pg 9.db is stuck peering for 4d, current state peering
    pg 9.14d is stuck peering for 4d, current state peering`

func TestCorrelateFindsSingleNodeFailure(t *testing.T) {
	findings := correlate(Snapshot{Health: "HEALTH_WARN", HealthDetail: realDetail})
	if len(findings) == 0 {
		t.Fatal("expected findings")
	}
	top := findings[0]
	if !strings.Contains(top.Summary, "node05") {
		t.Errorf("should name the failing node: %q", top.Summary)
	}
	if !strings.Contains(top.Summary, "6 个 OSD") {
		t.Errorf("should count the down OSDs: %q", top.Summary)
	}
	// 结论必须点破"多块盘同时坏不成立"
	if !strings.Contains(top.Why, "同时坏") {
		t.Errorf("reasoning should rule out simultaneous disk failures: %q", top.Why)
	}
	if !strings.Contains(top.Next, "host_check") {
		t.Errorf("next step should verify the node: %q", top.Next)
	}
}

func TestCorrelateFindsHeartbeatOffenders(t *testing.T) {
	detail := `Slow OSD heartbeats on back from osd.105 [] (down) to osd.78 [] 9945.541 msec
    Slow OSD heartbeats on back from osd.98 [] to osd.105 [] (down) 10072.200 msec
    Slow OSD heartbeats on front from osd.105 [] (down) to osd.114 [] 9125.942 msec`
	findings := correlate(Snapshot{HealthDetail: detail})
	var found bool
	for _, f := range findings {
		if strings.Contains(f.Summary, "osd.105") {
			found = true
		}
	}
	if !found {
		t.Errorf("should identify the recurring OSD: %+v", findings)
	}
}

func TestCorrelateSpotsSplitAcrossHosts(t *testing.T) {
	detail := `osd.1 (root=r,host=node-a) is down
    osd.2 (root=r,host=node-b) is down`
	findings := correlate(Snapshot{HealthDetail: detail})
	if len(findings) == 0 {
		t.Fatal("expected findings")
	}
	if !strings.Contains(findings[0].Summary, "分散") {
		t.Errorf("down OSDs on different hosts should be reported as spread out: %q", findings[0].Summary)
	}
}

func TestCorrelateHealthyNoFindings(t *testing.T) {
	if got := correlate(Snapshot{Health: "HEALTH_OK"}); len(got) != 0 {
		t.Errorf("healthy cluster should produce no findings: %+v", got)
	}
}

func TestShortHostName(t *testing.T) {
	if got := shortHostName("cloud-hdd05-node05-77876dc2-4900-4823-95c2-64641c9e7443"); got != "cloud-hdd05-node05" {
		t.Errorf("got %q", got)
	}
	if got := shortHostName("node03"); got != "node03" {
		t.Errorf("got %q", got)
	}
}
