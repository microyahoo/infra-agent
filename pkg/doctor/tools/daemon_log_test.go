package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

func TestDaemonLogFallsBackToRookLogDir(t *testing.T) {
	tr := newFakeRunner()
	tr.caps.LogDirs = []string{"/var/log/ceph", "/var/lib/rook/rook-ceph/logs"}
	tr.fail("node-3", []string{"tail", "-n", "300", "/var/log/ceph/ceph-osd.7.log"}, &access.ToolError{Kind: access.KindFailed, Message: "no such file"})
	tr.on("node-3", []string{"tail", "-n", "300", "/var/lib/rook/rook-ceph/logs/ceph-osd.7.log"}, "2026-10-23 15:00:00.000 osd.7 heartbeat_check: no reply\n")

	res, raw, err := NewDaemonLog(tr).Execute(context.Background(), json.RawMessage(`{"daemon":"osd","id":"7","lines":300}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*DaemonLogData)
	if !strings.Contains(d.Path, "/var/lib/rook/rook-ceph/logs") {
		t.Errorf("path = %q", d.Path)
	}
	if !strings.Contains(res.Summary, "rook-log") {
		t.Errorf("summary should name the source: %q", res.Summary)
	}
	if raw.Output == "" {
		t.Error("raw output missing")
	}
}

func TestDaemonLogRequiresDaemon(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewDaemonLog(tr).Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing daemon")
	}
}

func TestDaemonLogRejectsUnknownDaemon(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewDaemonLog(tr).Execute(context.Background(), json.RawMessage(`{"daemon":"evil"}`))
	if err == nil {
		t.Fatal("expected error for unknown daemon type")
	}
}

func TestDaemonLogFallsBackToKubectl(t *testing.T) {
	tr := newFakeRunner()
	tr.caps.LogDirs = nil
	tr.caps.Kubectl = true
	tr.on("node-3", []string{"kubectl", "-n", "rook-ceph", "logs", "rook-ceph-osd-7", "--tail", "100"}, "log line\n")

	res, _, err := NewDaemonLog(tr).Execute(context.Background(), json.RawMessage(`{"daemon":"osd","id":"7","lines":100}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Data.(*DaemonLogData).Source != "kubectl-logs" {
		t.Errorf("source = %q", res.Data.(*DaemonLogData).Source)
	}
}

func TestDaemonLogAllSourcesFail(t *testing.T) {
	tr := newFakeRunner()
	tr.caps.LogDirs = nil
	tr.caps.Kubectl = false
	_, _, err := NewDaemonLog(tr).Execute(context.Background(), json.RawMessage(`{"daemon":"osd","id":"7"}`))
	if err == nil {
		t.Fatal("expected error when no source works")
	}
}
