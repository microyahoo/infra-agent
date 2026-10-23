package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const sampleLog = `2026-10-23 15:00:00.000 osd.7 heartbeat_check: no reply
2026-10-23 15:00:01.000 osd.7 slow request 30s
2026-10-23 15:00:02.000 osd.7 heartbeat_check: no reply
`

func TestReadLogTail(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"tail", "-n", "500", "/var/log/ceph/fsid/ceph-osd.7.log"}, sampleLog)

	res, raw, err := NewReadLog(tr).Execute(context.Background(), json.RawMessage(
		`{"node":"node-3","path":"/var/log/ceph/fsid/ceph-osd.7.log","tail_lines":500}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*ReadLogData)
	if d.Matched != 3 {
		t.Errorf("matched = %d", d.Matched)
	}
	if !strings.Contains(d.Content, "heartbeat_check") {
		t.Errorf("content = %q", d.Content)
	}
	if raw.Command == "" {
		t.Error("raw missing")
	}
}

func TestReadLogGrepKeepsMatches(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"tail", "-n", "1000", "/var/log/ceph/fsid/ceph-osd.7.log"}, sampleLog)

	res, _, err := NewReadLog(tr).Execute(context.Background(), json.RawMessage(
		`{"node":"node-3","path":"/var/log/ceph/fsid/ceph-osd.7.log","grep":"heartbeat_check"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*ReadLogData)
	if d.Matched != 2 {
		t.Errorf("matched = %d, want 2", d.Matched)
	}
	if strings.Contains(d.Content, "slow request") {
		t.Errorf("non-matching line leaked: %q", d.Content)
	}
}

func TestReadLogRejectsPathOutsideWhitelist(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewReadLog(tr).Execute(context.Background(), json.RawMessage(
		`{"node":"node-3","path":"/etc/shadow"}`))
	if err == nil {
		t.Fatal("expected rejection for path outside whitelist")
	}
}

func TestReadLogRejectsTraversal(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewReadLog(tr).Execute(context.Background(), json.RawMessage(
		`{"node":"node-3","path":"/var/log/ceph/../../../etc/passwd"}`))
	if err == nil {
		t.Fatal("expected rejection for traversal")
	}
}

func TestReadLogCapsLines(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"tail", "-n", "5000", "/var/log/ceph/a.log"}, "x")

	_, _, err := NewReadLog(tr).Execute(context.Background(), json.RawMessage(
		`{"node":"node-3","path":"/var/log/ceph/a.log","tail_lines":999999}`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadLogRejectsUnknownNode(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewReadLog(tr).Execute(context.Background(), json.RawMessage(
		`{"node":"node-99","path":"/var/log/ceph/a.log"}`))
	if err == nil {
		t.Fatal("expected error for unknown node")
	}
}

func TestReadLogGrepNoMatchHints(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"tail", "-n", "1000", "/var/log/ceph/a.log"}, sampleLog)

	res, _, err := NewReadLog(tr).Execute(context.Background(), json.RawMessage(
		`{"node":"node-3","path":"/var/log/ceph/a.log","grep":"nothing_here"}`))
	if err != nil {
		t.Fatal(err)
	}
	if d := res.Data.(*ReadLogData); d.Matched != 0 || d.Content != "" {
		t.Errorf("expected no matches, got %+v", d)
	}
	if !strings.Contains(strings.Join(res.Hints, " "), "未找到") {
		t.Errorf("expected not-found hint, got %v", res.Hints)
	}
}

func TestReadLogAllowsRookLogsPath(t *testing.T) {
	tr := newFakeRunner()
	path := "/var/lib/rook/rook-ceph/logs/ceph-osd.7.log"
	tr.on("node-3", []string{"tail", "-n", "500", path}, sampleLog)

	_, _, err := NewReadLog(tr).Execute(context.Background(), json.RawMessage(
		`{"node":"node-3","path":"`+path+`"}`))
	if err != nil {
		t.Fatal(err)
	}
}
