package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/internal/access"
)

func TestRunCommandExecutesArgv(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "quorum_status", "-f", "json"}, `{"quorum":[0,1,2]}`)

	res, raw, err := NewRunCommand(tr).Execute(context.Background(),
		json.RawMessage(`{"argv":["ceph","quorum_status","-f","json"]}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(map[string]any)
	if !strings.Contains(d["output"].(string), "quorum") {
		t.Errorf("output = %v", d["output"])
	}
	if raw.Command == "" {
		t.Error("raw command missing")
	}
	if !strings.Contains(res.Summary, "quorum_status") {
		t.Errorf("summary = %q", res.Summary)
	}
}

// 整条命令拼成字符串的写法必须被拒绝：参数边界丢失会让策略层无法逐项校验。
func TestRunCommandRejectsConcatenatedString(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewRunCommand(tr).Execute(context.Background(),
		json.RawMessage(`{"argv":["ceph osd out 7"]}`))
	if err == nil {
		t.Fatal("expected rejection for concatenated command string")
	}
	if !strings.Contains(err.Error(), "参数数组") {
		t.Errorf("error should explain argv form: %v", err)
	}
}

// 变更命令必须被策略层拦住，且错误要能说明原因。
func TestRunCommandBlocksMutationViaPolicy(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewRunCommand(tr).Execute(context.Background(),
		json.RawMessage(`{"argv":["ceph","osd","out","7"]}`))
	if err == nil {
		t.Fatal("LEAK: mutating command must be blocked")
	}
}

func TestRunCommandRequiresKnownNode(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewRunCommand(tr).Execute(context.Background(),
		json.RawMessage(`{"argv":["ceph","-s"],"node":"node-99"}`))
	if err == nil {
		t.Fatal("expected unknown node error")
	}
}

func TestRunCommandTruncatesLongOutput(t *testing.T) {
	tr := newFakeRunner()
	var lines []string
	for i := 0; i < 500; i++ {
		lines = append(lines, "line")
	}
	tr.on("node-3", []string{"ceph", "log", "last", "500"}, strings.Join(lines, "\n"))

	res, _, err := NewRunCommand(tr).Execute(context.Background(),
		json.RawMessage(`{"argv":["ceph","log","last","500"]}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(map[string]any)
	if !d["truncated"].(bool) {
		t.Error("expected truncation flag")
	}
	if d["lines"].(int) > 120 {
		t.Errorf("lines = %v, want <= 120", d["lines"])
	}
}

// 这个工具存在的意义：预设工具覆盖不到的命令也能查。
func TestRunCommandReachesUncoveredCommands(t *testing.T) {
	cases := [][]string{
		{"radosgw-admin", "bucket", "list"},
		{"ceph", "fs", "status"},
		{"chronyc", "tracking"},
		{"ethtool", "-S", "eth0"},
		{"journalctl", "-u", "ceph-mon@a", "-n", "50", "--no-pager"},
	}
	p := access.DefaultPolicy()
	for _, argv := range cases {
		if err := p.Check(argv); err != nil {
			t.Errorf("read-only exploration blocked: %v (%v)", argv, err)
		}
	}
}
