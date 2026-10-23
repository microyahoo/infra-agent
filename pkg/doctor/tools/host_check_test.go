package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestHostCheckCollectsBasics(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"uptime"}, " 15:00:00 up 30 days, load average: 12.5, 11.0, 9.8\n")
	tr.on("node-3", []string{"free", "-m"}, "              total used free\nMem:          64000 61000 3000\n")
	tr.on("node-3", []string{"df", "-h"}, "Filesystem Size Used Avail Use% Mounted on\n/dev/sda1 100G 95G 5G 95% /\n")

	res, raw, err := NewHostCheck(tr).Execute(context.Background(), json.RawMessage(`{"node":"node-3"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*HostCheckData)
	if !strings.Contains(d.Load, "12.5") {
		t.Errorf("load = %q", d.Load)
	}
	if !strings.Contains(d.Memory, "61000") {
		t.Errorf("memory = %q", d.Memory)
	}
	if raw.Node != "node-3" {
		t.Errorf("raw node = %q", raw.Node)
	}
	if !strings.Contains(strings.Join(res.Hints, " "), "负载") {
		t.Errorf("expected load hint, got %v", res.Hints)
	}
}

func TestHostCheckRequiresNode(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewHostCheck(tr).Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHostCheckQuietHostNoHints(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"uptime"}, " 15:00:00 up 30 days, load average: 1.2, 1.0, 0.9\n")
	tr.on("node-3", []string{"free", "-m"}, "Mem: 64000 21000 43000\n")
	tr.on("node-3", []string{"df", "-h"}, "/dev/sda1 100G 40G 60G 40% /\n")

	res, _, err := NewHostCheck(tr).Execute(context.Background(), json.RawMessage(`{"node":"node-3"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hints) != 0 {
		t.Errorf("quiet host should not hint, got %v", res.Hints)
	}
}

func TestHostCheckFullDiskHints(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"uptime"}, " 15:00:00 up 30 days, load average: 1.2, 1.0, 0.9\n")
	tr.on("node-3", []string{"free", "-m"}, "Mem: 64000 21000 43000\n")
	tr.on("node-3", []string{"df", "-h"}, "Filesystem Size Used Avail Use% Mounted on\n/dev/sda1 100G 100G 0 100% /\n")

	res, _, err := NewHostCheck(tr).Execute(context.Background(), json.RawMessage(`{"node":"node-3"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Hints, " "), "100%") {
		t.Errorf("expected full-disk hint, got %v", res.Hints)
	}
}

func TestHostCheckDegradesWhenCommandFails(t *testing.T) {
	tr := newFakeRunner()
	tr.fail("node-3", []string{"uptime"}, errString("uptime: command not found"))
	tr.on("node-3", []string{"free", "-m"}, "Mem: 64000 21000 43000\n")

	res, raw, err := NewHostCheck(tr).Execute(context.Background(), json.RawMessage(`{"node":"node-3"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*HostCheckData)
	if d.Load != "" || d.Load1 != 0 {
		t.Errorf("load should stay empty, got %+v", d)
	}
	if d.Memory == "" {
		t.Error("memory should still be collected")
	}
	if raw == nil || raw.Node != "node-3" {
		t.Errorf("raw = %+v", raw)
	}
}
