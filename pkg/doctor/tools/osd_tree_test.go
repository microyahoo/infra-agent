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

// 某些 Ceph 版本的 ceph osd df 不直接给 utilization，只给 kb / kb_used。
// 这种形态下必须能算出使用率，否则热点检测会静默失效。
func TestOSDTreeComputesUtilizationFromKB(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "osd", "df", "-f", "json"}, `{"nodes":[
      {"id":7,"name":"osd.7","type":"osd","status":"up","kb":1000,"kb_used":950},
      {"id":8,"name":"osd.8","type":"osd","status":"up","kb":1000,"kb_used":400}]}`)

	res, _, err := NewOSDTree(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*OSDTreeData)
	if d.OSDs[0].Utilization != 95 {
		t.Errorf("osd.7 utilization = %v, want 95", d.OSDs[0].Utilization)
	}
	if len(d.Hotspots) != 1 || d.Hotspots[0].ID != 7 {
		t.Errorf("expected osd.7 hotspot, got %+v", d.Hotspots)
	}
}

// Ceph 的 utilization 字段是 0-1 小数。若不换算，热点阈值永远不触发，
// 摘要也会显示成 0.0%——这是不会报错的静默错误。
func TestOSDTreeScalesFractionalUtilization(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "osd", "df", "-f", "json"}, `{"nodes":[
      {"id":9,"name":"osd.9","type":"osd","status":"up","kb":19531825152,"kb_used":18000000000,"utilization":0.92},
      {"id":19,"name":"osd.19","type":"osd","status":"up","kb":19531825152,"kb_used":1030184,"utilization":0.0052}]}`)

	res, _, err := NewOSDTree(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*OSDTreeData)
	if d.OSDs[0].Utilization != 92 {
		t.Errorf("osd.9 utilization = %v, want 92", d.OSDs[0].Utilization)
	}
	if d.OSDs[1].Utilization < 0.5 || d.OSDs[1].Utilization > 0.6 {
		t.Errorf("osd.19 utilization = %v, want ~0.52", d.OSDs[1].Utilization)
	}
	if len(d.Hotspots) != 1 || d.Hotspots[0].ID != 9 {
		t.Errorf("expected osd.9 hotspot, got %+v", d.Hotspots)
	}
}
