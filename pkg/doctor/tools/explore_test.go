package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const lsOutput = `total 4313332
drwxr-xr-x 16 root    root        4096 Oct 13 17:55 .
drwxr-xr-x  3 root    root          23 Oct 13 17:52 ..
drwxr-xr-x  2 polkitd polkitd      129 Oct 23 14:32 84b8e0cd_0606d192
-rw-r--r--  1 polkitd polkitd  1516803 Oct 26 16:01 ceph-osd.9.log
-rw-------  1 root    root         152 Oct 23 14:34 client.admin.keyring
drwxr-xr-x  2 polkitd polkitd     8192 Oct 26 08:02 log
`

func TestExploreListsDir(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ls", "-la", "/var/lib/rook/rook-ceph"}, lsOutput)

	res, _, err := NewExplore(tr).Execute(context.Background(),
		json.RawMessage(`{"dir":"/var/lib/rook/rook-ceph"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*ExploreData)
	if len(d.Entries) != 4 {
		t.Fatalf("entries = %d, want 4 (skip . and ..): %+v", len(d.Entries), d.Entries)
	}
	// 目录排前面
	if !d.Entries[0].IsDir {
		t.Error("directories should sort first")
	}
	if !strings.Contains(res.Summary, "子目录") {
		t.Errorf("summary = %q", res.Summary)
	}
	// 必须指出下一步去哪，而不是让模型继续猜
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "read_log") {
		t.Errorf("should hint at read_log for found log files: %v", res.Hints)
	}
}

func TestExploreFindsLogs(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"find", "/var/lib/rook/rook-ceph/log", "-name", "*.log", "-type", "f", "-printf", "%s %p\n"},
		"1516803 /var/lib/rook/rook-ceph/log/ceph-osd.9.log\n2048 /var/lib/rook/rook-ceph/log/ceph-mon.a.log\n")

	res, _, err := NewExplore(tr).Execute(context.Background(),
		json.RawMessage(`{"dir":"/var/lib/rook/rook-ceph/log","find_pattern":"*.log"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*ExploreData)
	if len(d.Files) != 2 {
		t.Fatalf("files = %d", len(d.Files))
	}
	// 按大小降序，最大的最可能是活跃日志
	if d.Files[0].Size != 1516803 {
		t.Errorf("files not sorted by size: %+v", d.Files)
	}
}

// 只允许探查诊断相关目录，不能全盘遍历。
func TestExploreRejectsOutsideRoots(t *testing.T) {
	tr := newFakeRunner()
	for _, dir := range []string{"/etc/shadow", "/root", "/var/log/../../etc", "/"} {
		_, _, err := NewExplore(tr).Execute(context.Background(),
			json.RawMessage(`{"dir":"`+dir+`"}`))
		if err == nil {
			t.Errorf("LEAK: explore allowed on %q", dir)
		}
	}
}

func TestExploreAllowsKnownRoots(t *testing.T) {
	for _, dir := range []string{
		"/var/log", "/var/log/ceph", "/var/lib/rook", "/var/lib/rook/rook-ceph/log",
		"/var/lib/ceph", "/etc/ceph",
	} {
		if !underExploreRoot(dir) {
			t.Errorf("should be explorable: %s", dir)
		}
	}
}

func TestExploreRequiresDirOrPattern(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewExplore(tr).Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

// find 的模式不能含路径分隔符，避免借它枚举任意路径。
func TestExploreRejectsPatternWithPath(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewExplore(tr).Execute(context.Background(),
		json.RawMessage(`{"dir":"/var/log","find_pattern":"../../etc/*"}`))
	if err == nil {
		t.Fatal("expected rejection")
	}
}
