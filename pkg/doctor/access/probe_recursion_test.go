package access

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 回归：能力探测本身是产生 Capabilities 的过程，绝不能经过路由改写。
// 曾经的实现让探测走 executor → realExecute → rewrite → Capabilities → 探测，
// 形成无限递归，真实运行时会直接卡死。
func TestProbeUsesSenderAndNeverReentersCapabilities(t *testing.T) {
	cc := &clusterConn{cfg: Config{
		Host: "node-3", CommandTimeout: time.Second, Policy: DefaultPolicy(),
	}}
	c := newCluster(cc)

	var probed []string
	c.setSender(func(_ context.Context, node string, argv []string) (Output, error) {
		probed = append(probed, strings.Join(argv, " "))
		switch strings.Join(argv, " ") {
		case "ceph --version":
			return Output{Stdout: "ceph version 18.2.4 (x) reef (stable)", Node: node}, nil
		default:
			return Output{Stderr: "not found", ExitCode: 127, Node: node}, nil
		}
	})

	done := make(chan Capabilities, 1)
	go func() { done <- c.Capabilities(context.Background(), "node-3") }()

	select {
	case caps := <-done:
		if caps.Backend != "host-ceph" {
			t.Errorf("backend = %q, want host-ceph", caps.Backend)
		}
		if len(probed) == 0 {
			t.Error("sender should have been used for probing")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Capabilities hung: probe must not recurse through rewrite")
	}
}

// 探测命令仍然要过只读策略。
func TestProbeRespectsPolicy(t *testing.T) {
	cc := &clusterConn{cfg: Config{Host: "node-3", CommandTimeout: time.Second, Policy: DefaultPolicy()}}
	c := newCluster(cc)

	var attempted []string
	c.setSender(func(_ context.Context, _ string, argv []string) (Output, error) {
		attempted = append(attempted, strings.Join(argv, " "))
		return Output{}, nil
	})
	c.Capabilities(context.Background(), "node-3")

	for _, a := range attempted {
		if err := DefaultPolicy().Check(strings.Fields(a)); err != nil {
			t.Errorf("probe issued a command the policy rejects: %q (%v)", a, err)
		}
	}
}
