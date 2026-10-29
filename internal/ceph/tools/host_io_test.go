package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/internal/access"
)

func TestHostIORunsIostatDmesgSmart(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"iostat", "-x", "1", "3"}, "Device  r/s  w/s  r_await  w_await  %util\nsdc  10  200  5.2  420.1  99.8\n")
	tr.on("node-3", []string{"dmesg", "-T"}, "[Fri Oct 23 15:00:00 2026] I/O error, dev sdc, sector 123\n")
	tr.on("node-3", []string{"smartctl", "-a", "/dev/sdc"}, "SMART overall-health self-assessment test result: FAILED\n")

	res, raw, err := NewHostIO(tr).Execute(context.Background(), json.RawMessage(`{"node":"node-3","device":"sdc"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*HostIOData)
	if d.Iostat == "" {
		t.Error("iostat missing")
	}
	if !strings.Contains(d.Dmesg, "I/O error") {
		t.Errorf("dmesg = %q", d.Dmesg)
	}
	if !strings.Contains(d.Smart, "FAILED") {
		t.Errorf("smart = %q", d.Smart)
	}
	if !d.SmartAvailable {
		t.Error("smart should be available")
	}
	if !strings.Contains(res.Summary, "sdc") {
		t.Errorf("summary = %q", res.Summary)
	}
	// 三项异常都应写进一句话结论。
	for _, want := range []string{"利用率", "I/O 错误", "SMART"} {
		if !strings.Contains(res.Summary, want) {
			t.Errorf("summary missing %q: %q", want, res.Summary)
		}
	}
	if raw.Node != "node-3" {
		t.Errorf("raw node = %q", raw.Node)
	}
	if !strings.Contains(raw.Command, "iostat") {
		t.Errorf("raw should prefer iostat, got %q", raw.Command)
	}
}

func TestHostIOFiltersDmesgForDevice(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"iostat", "-x", "1", "3"}, "x")
	tr.on("node-3", []string{"dmesg", "-T"}, "[t] I/O error, dev sda, sector 1\n[t] I/O error, dev sdc, sector 2\n")
	tr.on("node-3", []string{"smartctl", "-a", "/dev/sdc"}, "ok")

	res, _, err := NewHostIO(tr).Execute(context.Background(), json.RawMessage(`{"node":"node-3","device":"sdc"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*HostIOData)
	if strings.Contains(d.Dmesg, "sda") {
		t.Errorf("dmesg should be filtered to sdc only: %q", d.Dmesg)
	}
	if !strings.Contains(d.Dmesg, "sdc") {
		t.Errorf("expected sdc line: %q", d.Dmesg)
	}
}

func TestHostIOUnavailableSmart(t *testing.T) {
	tr := newFakeRunner()
	tr.caps.HostTools["smartctl"] = false
	tr.on("node-3", []string{"iostat", "-x", "1", "3"}, "x")
	tr.on("node-3", []string{"dmesg", "-T"}, "")

	res, _, err := NewHostIO(tr).Execute(context.Background(), json.RawMessage(`{"node":"node-3","device":"sdc"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*HostIOData)
	if d.SmartAvailable {
		t.Error("smart should be marked unavailable")
	}
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "smartctl") {
		t.Errorf("expected hint about manual smartctl, got %v", res.Hints)
	}
	if !strings.Contains(joined, "smartctl -a /dev/sdc") {
		t.Errorf("hint should give the exact manual command, got %v", res.Hints)
	}
}

func TestHostIORequiresNodeAndDevice(t *testing.T) {
	tr := newFakeRunner()
	for _, args := range []string{`{}`, `{"node":"node-3"}`, `{"device":"sdc"}`} {
		_, _, err := NewHostIO(tr).Execute(context.Background(), json.RawMessage(args))
		if err == nil {
			t.Errorf("expected error for args %s", args)
		}
	}
}

func TestHostIORejectsUnknownNode(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewHostIO(tr).Execute(context.Background(), json.RawMessage(`{"node":"node-99","device":"sdc"}`))
	if err == nil {
		t.Fatal("expected error for unknown node")
	}
	var te *access.ToolError
	if !errors.As(err, &te) || te.Kind != access.KindUnreachable {
		t.Errorf("expected KindUnreachable, got %v", err)
	}
}

// device 带 /dev/ 前缀时应归一化，并且 iostat 未被过滤为空时原样保留。
func TestHostIONormalizesDevice(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"iostat", "-x", "1", "3"}, "Device  r/s  w/s\nsdc  1  2\n")
	tr.on("node-3", []string{"dmesg", "-T"}, "")
	tr.on("node-3", []string{"smartctl", "-a", "/dev/sdc"}, "ok")

	res, _, err := NewHostIO(tr).Execute(context.Background(), json.RawMessage(`{"node":"node-3","device":"/dev/sdc"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*HostIOData)
	if d.Device != "sdc" {
		t.Errorf("device = %q", d.Device)
	}
	if !strings.Contains(d.Iostat, "sdc") && !strings.Contains(d.Iostat, "Device") {
		t.Errorf("iostat = %q", d.Iostat)
	}
	if !strings.Contains(res.Summary, "node-3/sdc") {
		t.Errorf("summary = %q", res.Summary)
	}
}

// 所有数据源失败时必须说"无法判断"，不能说"未发现异常"。
// 后者会被模型当成"磁盘没问题"，从而排除正确的排查方向。
func TestHostIOAllFailedDoesNotClaimHealthy(t *testing.T) {
	tr := newFakeRunner()
	tr.fail("node-3", []string{"iostat", "-x", "1", "3"}, &access.ToolError{Kind: access.KindUnreachable, Message: "ssh failed"})
	tr.fail("node-3", []string{"dmesg", "-T"}, &access.ToolError{Kind: access.KindUnreachable, Message: "ssh failed"})
	tr.caps.HostTools["smartctl"] = false

	res, _, err := NewHostIO(tr).Execute(context.Background(),
		json.RawMessage(`{"node":"node-3","device":"sdc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Summary, "未发现明显的磁盘层异常") {
		t.Errorf("must not claim health when nothing was collected: %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "无法采集") && !strings.Contains(res.Summary, "不能据此判断") {
		t.Errorf("summary should state observation failure: %q", res.Summary)
	}
}
