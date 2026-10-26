package access

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestClusterRunRejectsPolicyViolation(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		return fakeSession{stdout: "x"}
	})
	_, err := c.Run(context.Background(), "node-3", []string{"ceph", "osd", "out", "7"})
	if err == nil {
		t.Fatal("expected policy rejection")
	}
	var te *ToolError
	if !errors.As(err, &te) || te.Kind != KindFailed {
		t.Errorf("expected KindFailed tool error, got %v", err)
	}
}

func TestClusterRunReturnsStdout(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		return fakeSession{stdout: "HEALTH_OK\n"}
	})
	out, err := c.Run(context.Background(), "node-3", []string{"ceph", "-s"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Stdout != "HEALTH_OK\n" {
		t.Errorf("stdout = %q", out.Stdout)
	}
	if out.Node != "node-3" {
		t.Errorf("node = %q", out.Node)
	}
	if out.Command == "" {
		t.Error("command should be recorded for audit")
	}
}

func TestClusterRunClassifiesTimeout(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		return fakeSession{err: context.DeadlineExceeded}
	})
	_, err := c.Run(context.Background(), "node-3", []string{"ceph", "-s"})
	var te *ToolError
	if !errors.As(err, &te) || te.Kind != KindTimeout {
		t.Fatalf("expected KindTimeout, got %v", err)
	}
}

func TestClusterRunClassifiesUnreachable(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		return fakeSession{err: errors.New("ssh: handshake failed: connection refused")}
	})
	_, err := c.Run(context.Background(), "node-3", []string{"ceph", "-s"})
	var te *ToolError
	if !errors.As(err, &te) || te.Kind != KindUnreachable {
		t.Fatalf("expected KindUnreachable, got %v", err)
	}
}

func TestClusterRunTruncatesLargeOutput(t *testing.T) {
	big := make([]byte, 400*1024)
	for i := range big {
		big[i] = 'a'
	}
	c := newTestCluster(t, func(argv []string) fakeSession {
		return fakeSession{stdout: string(big)}
	})
	out, err := c.Run(context.Background(), "node-3", []string{"ceph", "log", "last", "10000"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Truncated {
		t.Error("expected truncated")
	}
	if len(out.Stdout) > 300*1024 {
		t.Errorf("output too large after truncation: %d", len(out.Stdout))
	}
}

func TestClusterCachesCapabilities(t *testing.T) {
	calls := 0
	c := newTestCluster(t, func(argv []string) fakeSession {
		if len(argv) > 0 && argv[0] == "ceph" && len(argv) > 1 && argv[1] == "--version" {
			calls++
			return fakeSession{stdout: "ceph version 18.2.4 (x) reef (stable)"}
		}
		return fakeSession{exit: 127}
	})
	c.Capabilities(context.Background(), "node-3")
	c.Capabilities(context.Background(), "node-3")
	if calls != 1 {
		t.Errorf("capability probe ran %d times, want 1 (should cache)", calls)
	}
}

func TestClusterRejectsUnknownNode(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		return fakeSession{stdout: "x"}
	})
	_, err := c.Run(context.Background(), "node-unknown", []string{"ceph", "-s"})
	if err == nil {
		t.Fatal("expected error for unknown node")
	}
}

func TestClusterCapabilitiesProbesAndCaches(t *testing.T) {
	calls := 0
	c := newTestCluster(t, func(argv []string) fakeSession {
		if len(argv) > 0 && argv[0] == "ceph" && len(argv) > 1 && argv[1] == "--version" {
			calls++
			return fakeSession{stdout: "ceph version 18.2.4 (x) reef (stable)"}
		}
		return fakeSession{exit: 127}
	})
	caps := c.Capabilities(context.Background(), "node-3")
	if caps.Backend != "host-ceph" || !caps.CephCLI || caps.CephVersion != "18.2.4" {
		t.Errorf("unexpected caps: %+v", caps)
	}
	// 第二次必须命中缓存
	c.Capabilities(context.Background(), "node-3")
	if calls != 1 {
		t.Errorf("capability probe ran %d times, want 1 (should cache)", calls)
	}
}

func TestClusterTopologyRegistersNodesAndCaches(t *testing.T) {
	treeCalls := 0
	c := newTestCluster(t, func(argv []string) fakeSession {
		key := strings.Join(argv, " ")
		switch {
		case key == "ceph fsid":
			return fakeSession{stdout: "fsid-abc\n"}
		case key == "ceph osd tree -f json":
			treeCalls++
			return fakeSession{stdout: osdTreeJSON}
		case key == "ceph osd metadata 7 -f json":
			return fakeSession{stdout: osdMetadata7JSON}
		case key == "ceph osd metadata 8 -f json":
			return fakeSession{exit: 1}
		}
		return fakeSession{exit: 1}
	})
	c.caps["node-3"] = Capabilities{CephCLI: true, Backend: "host-ceph", HostTools: map[string]bool{}}

	topo, err := c.Topology(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if topo.FSID != "fsid-abc" {
		t.Errorf("FSID = %q", topo.FSID)
	}
	if len(topo.OSDs) != 2 || topo.OSDs[0].Device != "sdc" {
		t.Errorf("unexpected osds: %+v", topo.OSDs)
	}
	// 拓扑发现的节点必须登记，以便后续直接 Run
	if !c.nodes["node-3"] {
		t.Error("node-3 should be registered")
	}
	again, err := c.Topology(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again != topo {
		t.Error("Topology should return the cached instance")
	}
	if treeCalls != 1 {
		t.Errorf("osd tree fetched %d times, want 1 (should cache)", treeCalls)
	}
}

func TestClusterNodesIncludesEntryAndDiscovered(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		return fakeSession{exit: 1}
	})
	c.nodes["node-3"] = true
	c.nodes["node-4"] = true
	got := c.Nodes()
	if len(got) != 2 {
		t.Fatalf("Nodes() = %v", got)
	}
	seen := map[string]bool{}
	for _, n := range got {
		seen[n] = true
	}
	if !seen["node-3"] || !seen["node-4"] {
		t.Errorf("Nodes() = %v", got)
	}
}

// —— 路由（rewrite）测试 ——

func TestRewriteHostCephUnchanged(t *testing.T) {
	c := newTestCluster(t, func([]string) fakeSession { return fakeSession{} })
	c.caps["node-3"] = Capabilities{CephCLI: true, Backend: "host-ceph", HostTools: map[string]bool{}}
	got, err := c.rewrite(context.Background(), "node-3", []string{"ceph", "-s"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "ceph" {
		t.Errorf("got %v", got)
	}
}

func TestRewriteLogOnlyReturnsUnavailable(t *testing.T) {
	c := newTestCluster(t, func([]string) fakeSession { return fakeSession{} })
	c.caps["node-3"] = Capabilities{CephCLI: false, Backend: "log-only", HostTools: map[string]bool{}}
	_, err := c.rewrite(context.Background(), "node-3", []string{"ceph", "-s"})
	if err == nil {
		t.Fatal("expected unavailable")
	}
	var te *ToolError
	if !errors.As(err, &te) || te.Kind != KindUnavailable {
		t.Fatalf("got %v", err)
	}
}

func TestRewriteNonCephAlwaysHost(t *testing.T) {
	c := newTestCluster(t, func([]string) fakeSession { return fakeSession{} })
	c.caps["node-3"] = Capabilities{CephCLI: false, Backend: "log-only", HostTools: map[string]bool{}}
	got, err := c.rewrite(context.Background(), "node-3", []string{"iostat", "-x", "1", "3"})
	if err != nil {
		t.Fatalf("host tools must not be routed to containers: %v", err)
	}
	if len(got) != 4 || got[0] != "iostat" {
		t.Errorf("got %v", got)
	}
}

func TestRewriteToolboxWrapsKubectlExec(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		if argv[0] == "kubectl" && containsAll(argv, "get", "pod") {
			return fakeSession{stdout: "rook-ceph-tools-abc\n"}
		}
		return fakeSession{}
	})
	c.caps["node-3"] = Capabilities{CephCLI: true, Backend: "toolbox", Kubectl: true, HostTools: map[string]bool{}}
	got, err := c.rewrite(context.Background(), "node-3", []string{"ceph", "-s"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "kubectl" || !containsAll(got, "exec", "--", "ceph", "-s") {
		t.Errorf("got %v", got)
	}
}

func TestRewriteContainerExecWrapsCrictl(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		if argv[0] == "crictl" && argv[1] == "ps" {
			return fakeSession{stdout: "abc123\n"}
		}
		return fakeSession{}
	})
	c.caps["node-3"] = Capabilities{CephCLI: true, Backend: "container-exec", ContainerRT: "crictl", HostTools: map[string]bool{}}
	got, err := c.rewrite(context.Background(), "node-3", []string{"ceph", "-s"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "crictl" || got[1] != "exec" || got[2] != "abc123" {
		t.Errorf("got %v", got)
	}
}

func TestRewriteContainerExecWrapsDocker(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		if argv[0] == "docker" && argv[1] == "ps" {
			return fakeSession{stdout: "def456\n"}
		}
		return fakeSession{}
	})
	c.caps["node-3"] = Capabilities{CephCLI: true, Backend: "container-exec", ContainerRT: "docker", HostTools: map[string]bool{}}
	got, err := c.rewrite(context.Background(), "node-3", []string{"ceph", "-s"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docker", "exec", "def456", "ceph", "-s"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestRewriteContainerExecNonCephStaysHost(t *testing.T) {
	c := newTestCluster(t, func([]string) fakeSession { return fakeSession{} })
	c.caps["node-3"] = Capabilities{CephCLI: true, Backend: "container-exec", ContainerRT: "crictl", HostTools: map[string]bool{}}
	got, err := c.rewrite(context.Background(), "node-3", []string{"tail", "-n", "100", "/var/log/ceph/ceph.log"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "tail" {
		t.Errorf("host tools must not be routed to containers: %v", got)
	}
}

func TestRewriteCachesContainerID(t *testing.T) {
	psCalls := 0
	c := newTestCluster(t, func(argv []string) fakeSession {
		if argv[0] == "crictl" && argv[1] == "ps" {
			psCalls++
			return fakeSession{stdout: "abc123\n"}
		}
		return fakeSession{}
	})
	c.caps["node-3"] = Capabilities{CephCLI: true, Backend: "container-exec", ContainerRT: "crictl", HostTools: map[string]bool{}}
	for i := 0; i < 3; i++ {
		if _, err := c.rewrite(context.Background(), "node-3", []string{"ceph", "-s"}); err != nil {
			t.Fatal(err)
		}
	}
	if psCalls != 1 {
		t.Errorf("mon container resolution ran %d times, want 1 (should cache)", psCalls)
	}
}

// TestRealExecuteRoutesCephIntoContainer 验证 Run 的完整链路：
// ceph argv 被改写为容器 exec，并按改写后的命令串执行。
func TestRewriteOutputPassesPolicyAfterRewrite(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		if argv[0] == "crictl" && argv[1] == "ps" {
			return fakeSession{stdout: "abc123\n"}
		}
		return fakeSession{}
	})
	c.caps["node-3"] = Capabilities{CephCLI: true, Backend: "container-exec", ContainerRT: "crictl", HostTools: map[string]bool{}}
	finalArgv, err := c.rewrite(context.Background(), "node-3", []string{"ceph", "-s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.conn.cfg.Policy.Check(finalArgv); err != nil {
		t.Fatalf("rewritten argv must pass policy: %v", err)
	}
	if BuildCommand(finalArgv) != "'crictl' 'exec' 'abc123' 'ceph' '-s'" {
		t.Errorf("unexpected command: %s", BuildCommand(finalArgv))
	}
}

func containsAll(argv []string, wants ...string) bool {
	set := map[string]bool{}
	for _, a := range argv {
		set[a] = true
	}
	for _, w := range wants {
		if !set[w] {
			return false
		}
	}
	return true
}

// —— 测试辅助 ——

type fakeSession struct {
	stdout string
	stderr string
	exit   int
	err    error
}

func newTestCluster(t *testing.T, handler func(argv []string) fakeSession) *Cluster {
	t.Helper()
	cc := &clusterConn{cfg: Config{CommandTimeout: time.Second, MaxOutputBytes: 256 * 1024, Policy: DefaultPolicy()}}
	c := newCluster(cc)
	c.nodes["node-3"] = true
	// 注意：这里不预置 c.caps——探测结果会按节点缓存，预置会让 Capabilities 缓存语义失效，
	// 也干扰 TestClusterCachesCapabilities。需要特定路由分支的用例自行写入 c.caps。
	fake := func(ctx context.Context, node string, argv []string) (Output, error) {
		if !c.nodes[node] {
			return Output{}, unavailable("未知节点 %q", node)
		}
		r := handler(argv)
		if r.err != nil {
			return Output{}, r.err
		}
		if r.exit != 0 {
			return Output{}, classifyError(nil, r.stderr, r.exit)
		}
		return Output{Stdout: r.stdout, Stderr: r.stderr, Node: node}, nil
	}
	// 两个缝都注入：Run 走 executor，探测/拓扑走 sender。
	c.setExecutor(fake)
	c.setSender(fake)
	return c
}

// Cluster 的缓存必须能在并发调用下安全使用：
// 工具层将来会并行发起诊断命令。
func TestClusterConcurrentRunIsSafe(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		return fakeSession{stdout: "ok"}
	})
	const n = 20
	done := make(chan error, n*3)
	for i := 0; i < n; i++ {
		go func() {
			_, err := c.Run(context.Background(), "node-3", []string{"ceph", "-s"})
			done <- err
		}()
		go func() {
			_ = c.Capabilities(context.Background(), "node-3")
			done <- nil
		}()
		go func() {
			_ = c.Nodes()
			done <- nil
		}()
	}
	for i := 0; i < n*3; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent call failed: %v", err)
		}
	}
}
