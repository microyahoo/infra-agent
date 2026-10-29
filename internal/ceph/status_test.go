package ceph

import "testing"

const sampleStatus = `{
  "fsid": "fsid-abc",
  "health": {"status": "HEALTH_WARN", "checks": {
    "OSD_DOWN": {"summary": {"message": "2 osds down"}}
  }},
  "monmap": {"num_mons": 3},
  "osdmap": {"num_osds": 10, "num_up_osds": 8, "num_in_osds": 10},
  "pgmap": {
    "num_pgs": 256,
    "pgs_by_state": [
      {"state_name": "active+clean", "count": 200},
      {"state_name": "active+undersized", "count": 56}
    ],
    "bytes_total": 2000,
    "bytes_used": 1000
  }
}`

func TestParseStatus(t *testing.T) {
	st, err := ParseStatus(sampleStatus)
	if err != nil {
		t.Fatal(err)
	}
	if st.FSID != "fsid-abc" || st.Health != "HEALTH_WARN" {
		t.Fatalf("basic fields: %+v", st)
	}
	if st.NumOSDs != 10 || st.NumOSDsUp != 8 || st.NumMons != 3 || st.NumPGs != 256 {
		t.Fatalf("counts: %+v", st)
	}
	if st.DegradedPGs != 56 {
		t.Errorf("degraded: got %d", st.DegradedPGs)
	}
	if st.UsedPercent != 50 {
		t.Errorf("used percent: got %v", st.UsedPercent)
	}
	if len(st.HealthChecks) != 1 || st.HealthChecks[0] != "OSD_DOWN" {
		t.Errorf("checks: %v", st.HealthChecks)
	}
	if len(st.HealthMessages) != 1 || st.HealthMessages[0] != "2 osds down" {
		t.Errorf("messages: %v", st.HealthMessages)
	}
}

func TestParseStatusRejectsGarbage(t *testing.T) {
	if _, err := ParseStatus("not json"); err == nil {
		t.Fatal("expected error")
	}
}

func TestStatusSummary(t *testing.T) {
	st, _ := ParseStatus(sampleStatus)
	got := st.Summary()
	want := "mon 3，OSD 10 总/8 up/10 in，PG 256（异常 56），容量 50.0%"
	if got != want {
		t.Errorf("summary:\n got %q\nwant %q", got, want)
	}
}
