package access

import (
	"context"
	"testing"
)

const osdTreeJSON = `{
  "nodes": [
    {"id": -1, "name": "default", "type": "root", "children": [-3]},
    {"id": -3, "name": "node-3", "type": "host", "children": [7, 8]},
    {"id": 7, "name": "osd.7", "type": "osd", "status": "up", "host": "node-3"},
    {"id": 8, "name": "osd.8", "type": "osd", "status": "down", "host": "node-3"}
  ],
  "strays": []
}`

const osdMetadata7JSON = `{
  "id": 7,
  "hostname": "node-3",
  "bluestore_bdev_devices": "sdc",
  "devices": "sdc",
  "osd_objectstore": "bluestore"
}`

const osdMetadataLVMJSON = `{
  "id": 9,
  "hostname": "node-4",
  "bluestore_bdev_devices": "/dev/mapper/ceph--9-abc-lv--block",
  "devices": "/dev/mapper/ceph--9-abc-lv--block",
  "osd_objectstore": "bluestore"
}`

func TestParseOSDTree(t *testing.T) {
	nodes, err := parseOSDTree(osdTreeJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d osd nodes, want 2", len(nodes))
	}
	if nodes[0].ID != 7 || nodes[0].Host != "node-3" || nodes[0].Status != "up" {
		t.Errorf("unexpected first node: %+v", nodes[0])
	}
	if nodes[1].Status != "down" {
		t.Errorf("expected second node down, got %q", nodes[1].Status)
	}
}

func TestParseOSDMetadataRawDevice(t *testing.T) {
	p, err := parseOSDMetadata(7, osdMetadata7JSON)
	if err != nil {
		t.Fatal(err)
	}
	if p.Host != "node-3" {
		t.Errorf("Host = %q", p.Host)
	}
	if p.Device != "sdc" {
		t.Errorf("Device = %q", p.Device)
	}
	if p.RawDevice != "sdc" {
		t.Errorf("RawDevice = %q", p.RawDevice)
	}
}

func TestParseOSDMetadataLVMDevice(t *testing.T) {
	p, err := parseOSDMetadata(9, osdMetadataLVMJSON)
	if err != nil {
		t.Fatal(err)
	}
	if p.RawDevice != "/dev/mapper/ceph--9-abc-lv--block" {
		t.Errorf("RawDevice = %q", p.RawDevice)
	}
	if !isLVMPath(p.RawDevice) {
		t.Error("expected LVM path detection")
	}
}

func TestIsLVMPath(t *testing.T) {
	cases := map[string]bool{
		"/dev/mapper/ceph--9-abc-lv--block": true,
		"/dev/ceph-abc/osd-block-xyz":       true,
		"sdc":                               false,
		"/dev/sdb":                          false,
	}
	for in, want := range cases {
		if got := isLVMPath(in); got != want {
			t.Errorf("isLVMPath(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseLsblkParent(t *testing.T) {
	if got := parseLsblkParent("sda\n"); got != "sda" {
		t.Errorf("got %q", got)
	}
	if got := parseLsblkParent("\n"); got != "" {
		t.Errorf("got %q", got)
	}
	if got := parseLsblkParent("sda\nsdb\n"); got != "sda" {
		t.Errorf("got %q", got)
	}
}

func TestBuildTopologyUsesMetadata(t *testing.T) {
	tr := &fakeTransport{
		responses: map[string]fakeResp{
			"ceph osd metadata 7 -f json": {stdout: osdMetadata7JSON},
			"ceph osd metadata 8 -f json": {stdout: `{"id":8,"hostname":"node-3","bluestore_bdev_devices":"sdd"}`},
		},
	}
	nodes := []osdNode{
		{ID: 7, Host: "node-3", Status: "up"},
		{ID: 8, Host: "node-3", Status: "down"},
	}
	topo := buildTopology(context.Background(), tr, "fsid-1", nodes)
	if topo.FSID != "fsid-1" {
		t.Errorf("FSID = %q", topo.FSID)
	}
	if len(topo.OSDs) != 2 {
		t.Fatalf("got %d placements", len(topo.OSDs))
	}
	if topo.OSDs[0].Device != "sdc" {
		t.Errorf("first device = %q", topo.OSDs[0].Device)
	}
	if len(topo.Nodes) != 1 || topo.Nodes[0].Name != "node-3" {
		t.Errorf("expected node-3 deduped, got %+v", topo.Nodes)
	}
	if len(topo.Nodes[0].OSDIDs) != 2 {
		t.Errorf("expected node-3 to hold 2 osds, got %v", topo.Nodes[0].OSDIDs)
	}
}
