package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 与工具实际使用的 jsonpath 输出一致：name \t node \t phase \t ready \t restarts \t waiting \t exit
const podListJSON = "rook-ceph-osd-6-abc\tcloud-hdd05-node05\tRunning\tfalse true\t14 0\tCrashLoopBackOff \t255 \n" +
	"rook-ceph-osd-17-def\tcloud-hdd05-node05\tRunning\tfalse\t12\tCrashLoopBackOff\t255\n" +
	"rook-ceph-mon-a-xyz\tcloud-hdd05-node01\tRunning\ttrue\t0\t\t\n"

// Rook 部署下，daemon 的问题首先体现在 pod 状态上。
// 这个工具存在的意义：让模型一次就看到 CrashLoop 与重启次数，
// 而不是用 run_command 摸索 kubectl。
func TestRookPodsSurfacesCrashLoop(t *testing.T) {
	tr := newFakeRunner()
	tr.caps.Kubectl = true
	tr.on("node-3", kubectlPodsArgv(), podListJSON)

	res, _, err := NewRookPods(tr).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*RookPodsData)
	if len(d.Pods) != 3 {
		t.Fatalf("pods = %d", len(d.Pods))
	}
	// 摘要要直接说出 CrashLoop 与重启次数
	if !strings.Contains(res.Summary, "CrashLoopBackOff") {
		t.Errorf("summary should name crashloop: %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "重启 14 次") {
		t.Errorf("summary should carry restart counts: %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "2 个 CrashLoopBackOff") {
		t.Errorf("summary should count them: %q", res.Summary)
	}
	// 退出码是重要线索
	if !strings.Contains(res.Summary, "退出码 255") {
		t.Errorf("summary should carry exit code: %q", res.Summary)
	}
}

func TestRookPodsHealthyNoHints(t *testing.T) {
	tr := newFakeRunner()
	tr.caps.Kubectl = true
	tr.on("node-3", kubectlPodsArgv(), "rook-ceph-mon-a\tcloud-hdd05-node01\tRunning\ttrue\t0\t\t\n")

	res, _, err := NewRookPods(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hints) != 0 {
		t.Errorf("healthy cluster should not hint: %v", res.Hints)
	}
}

func TestRookPodsDescribeHints(t *testing.T) {
	tr := newFakeRunner()
	tr.caps.Kubectl = true
	desc := `Name: rook-ceph-osd-6-abc
Events:
  Warning  Unhealthy  Liveness probe errored: container is in CONTAINER_EXITED state
  Normal   Pulled     Back-off restarting failed container`
	tr.on("node-3", []string{"kubectl", "-n", "rook-ceph", "describe", "pod", "rook-ceph-osd-6-abc"}, desc)

	res, _, err := NewRookPods(tr).Execute(context.Background(),
		json.RawMessage(`{"pod":"rook-ceph-osd-6-abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "liveness") {
		t.Errorf("should flag liveness probe failures: %v", res.Hints)
	}
}

func TestRookPodsWithoutKubectl(t *testing.T) {
	tr := newFakeRunner()
	tr.caps.Kubectl = false
	_, _, err := NewRookPods(tr).Execute(context.Background(), nil)
	if err == nil {
		t.Fatal("expected unavailable error")
	}
	if !strings.Contains(err.Error(), "daemon_log") {
		t.Errorf("error should suggest an alternative: %v", err)
	}
}

func kubectlPodsArgv() []string {
	return []string{"kubectl", "-n", "rook-ceph", "get", "pods", "-o", "jsonpath=" + podJSONPath}
}
