package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestOSDDetailReturnsStatusAndPlacement(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "tell", "osd.7", "status"}, `{"osd":{"osd":7,"up_since":"2026-10-23T10:00:00"},"state":"ACTIVE"}`)

	res, raw, err := NewOSDDetail(tr).Execute(context.Background(), json.RawMessage(`{"osd_id":7}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*OSDDetailData)
	if d.ID != 7 {
		t.Errorf("id = %d", d.ID)
	}
	if d.Host != "node-3" {
		t.Errorf("host = %q, want node-3 from topology", d.Host)
	}
	if d.Device != "sdc" {
		t.Errorf("device = %q, want sdc", d.Device)
	}
	if !strings.Contains(res.Summary, "osd.7") {
		t.Errorf("summary = %q", res.Summary)
	}
	if raw.Command == "" {
		t.Error("raw missing")
	}
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "host_io") {
		t.Errorf("expected host_io hint, got %v", res.Hints)
	}
}

func TestOSDDetailRequiresID(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewOSDDetail(tr).Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing osd_id")
	}
}

func TestOSDDetailUnknownOSDStillReturns(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "tell", "osd.99", "status"}, `{"state":"ACTIVE"}`)
	res, _, err := NewOSDDetail(tr).Execute(context.Background(), json.RawMessage(`{"osd_id":99}`))
	if err != nil {
		t.Fatalf("unknown osd should not fail: %v", err)
	}
	d := res.Data.(*OSDDetailData)
	if d.Device != "" {
		t.Errorf("device should be empty, got %q", d.Device)
	}
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "osd_tree") {
		t.Errorf("should hint osd_tree when placement unknown: %v", res.Hints)
	}
}
