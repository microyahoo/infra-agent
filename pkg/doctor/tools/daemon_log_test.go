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
	// Rook 的 pod 名带随机后缀，必须按标签选择器取日志。
	tr.on("node-3", []string{"kubectl", "-n", "rook-ceph", "logs", "-l", "ceph-osd-id=7", "-c", "osd", "--tail", "100"}, "log line\n")

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

// Rook 的 pod 名带随机后缀，按名字拼接必然 NotFound。
// 必须使用标签选择器。
func TestRookLogSelectorUsesLabels(t *testing.T) {
	sel, desc := rookLogSelector("osd", "9")
	if sel != "ceph-osd-id=9" {
		t.Errorf("osd selector = %q, want ceph-osd-id=9", sel)
	}
	if desc == "" {
		t.Error("describe should explain the selection")
	}
	sel, _ = rookLogSelector("mon", "a")
	if sel != "app=rook-ceph-mon" {
		t.Errorf("mon selector = %q", sel)
	}
	// osd 没给 id 时退回按 app 选择
	sel, _ = rookLogSelector("osd", "")
	if sel != "app=rook-ceph-osd" {
		t.Errorf("osd without id selector = %q", sel)
	}
}
