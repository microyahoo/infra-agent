package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const osdDFJSON = `{
  "nodes": [
    {"id": 7, "name": "osd.7", "type": "osd", "status": "up", "device_class": "ssd",
     "kb": 1048576, "kb_used": 950000, "utilization": 90.6, "var": 1.2, "reweight": 1.0, "crush_weight": 1.0, "pgs": 128},
    {"id": 8, "name": "osd.8", "type": "osd", "status": "up", "device_class": "ssd",
     "kb": 1048576, "kb_used": 620000, "utilization": 59.1, "var": 0.6, "reweight": 1.0, "crush_weight": 1.0, "pgs": 128},
    {"id": 9, "name": "osd.9", "type": "osd", "status": "down", "device_class": "hdd",
     "kb": 1048576, "kb_used": 100000, "utilization": 9.5, "var": 0.1, "reweight": 1.0, "crush_weight": 1.0, "pgs": 0}
  ]
}`

func TestOSDTreeParsesAndFlagsHotOSDs(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "osd", "df", "-f", "json"}, osdDFJSON)

	tool := NewOSDTree(tr)
	res, raw, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*OSDTreeData)
	if len(d.OSDs) != 3 {
		t.Fatalf("got %d osds", len(d.OSDs))
	}
	if d.OSDs[0].Utilization != 90.6 {
		t.Errorf("utilization = %v", d.OSDs[0].Utilization)
	}
	if len(d.Hotspots) == 0 {
		t.Fatal("expected osd.7 flagged as hotspot")
	}
	if d.Hotspots[0].ID != 7 {
		t.Errorf("hotspot = %d, want 7", d.Hotspots[0].ID)
	}
	if len(d.DownOSDs) != 1 || d.DownOSDs[0].ID != 9 {
		t.Errorf("down osds = %+v", d.DownOSDs)
	}
	if !strings.Contains(res.Summary, "osd.7") {
		t.Errorf("summary should name the hotspot: %q", res.Summary)
	}
	if raw.Command == "" {
		t.Error("raw command missing")
	}
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "host_io") {
		t.Errorf("expected host_io hint, got %v", res.Hints)
	}
}

func TestOSDTreeBalancedNoHotspot(t *testing.T) {
	balanced := `{"nodes":[
      {"id":1,"name":"osd.1","type":"osd","status":"up","utilization":60.0,"kb":100,"kb_used":60,"pgs":10},
      {"id":2,"name":"osd.2","type":"osd","status":"up","utilization":61.0,"kb":100,"kb_used":61,"pgs":10}]}`
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "osd", "df", "-f", "json"}, balanced)

	res, _, err := NewOSDTree(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*OSDTreeData)
	if len(d.Hotspots) != 0 {
		t.Errorf("expected no hotspot, got %+v", d.Hotspots)
	}
	for _, h := range res.Hints {
		if strings.Contains(h, "host_io") {
			t.Errorf("no hotspot should not hint host_io: %v", res.Hints)
		}
	}
}

func TestOSDTreeMapsDeviceFromTopology(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "osd", "df", "-f", "json"}, osdDFJSON)

	res, _, err := NewOSDTree(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*OSDTreeData)
	for _, o := range d.OSDs {
		if o.ID == 7 && o.Device != "sdc" {
			t.Errorf("osd.7 device = %q, want sdc (from topology)", o.Device)
		}
		if o.ID == 7 && o.Host != "node-3" {
			t.Errorf("osd.7 host = %q", o.Host)
		}
	}
}
