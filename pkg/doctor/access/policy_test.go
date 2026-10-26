package access

import (
	"strings"
	"testing"
)

func TestPolicyAllowsReadOnlyCeph(t *testing.T) {
	p := legacyAllowlistPolicy()
	allowed := [][]string{
		{"ceph", "-s"},
		{"ceph", "status", "-f", "json"},
		{"ceph", "health", "detail"},
		{"ceph", "df", "detail", "-f", "json"},
		{"ceph", "osd", "tree", "-f", "json"},
		{"ceph", "osd", "df", "-f", "json"},
		{"ceph", "osd", "metadata", "7"},
		{"ceph", "osd", "ok-to-stop", "7"},
		{"ceph", "pg", "stat"},
		{"ceph", "pg", "dump_stuck"},
		{"ceph", "pg", "2.4a", "query"},
		{"ceph", "tell", "osd.7", "dump_historic_ops"},
		{"ceph", "versions"},
		{"ceph", "log", "last", "200"},
		{"ceph", "mon", "stat"},
		{"ceph", "osd", "pool", "autoscale-status"},
		{"ceph", "fsid"},
	}
	for _, argv := range allowed {
		if err := p.Check(argv); err != nil {
			t.Errorf("expected allowed %v, got error: %v", argv, err)
		}
	}
}

func TestPolicyRejectsMutatingCeph(t *testing.T) {
	p := legacyAllowlistPolicy()
	rejected := [][]string{
		{"ceph", "osd", "out", "7"},
		{"ceph", "osd", "in", "7"},
		{"ceph", "osd", "rm", "7"},
		{"ceph", "osd", "crush", "remove", "osd.7"},
		{"ceph", "osd", "pool", "create", "x", "10"},
		{"ceph", "osd", "pool", "set", "x", "size", "3"},
		{"ceph", "osd", "pool", "delete", "x"},
		{"ceph", "osd", "set", "noout"},
		{"ceph", "config", "set", "global", "a", "b"},
		{"ceph", "tell", "osd.7", "injectargs", "--x"},
	}
	for _, argv := range rejected {
		if err := p.Check(argv); err == nil {
			t.Errorf("expected rejected %v, got nil", argv)
		}
	}
}

func TestPolicyRejectsUnknownBinary(t *testing.T) {
	p := legacyAllowlistPolicy()
	for _, argv := range [][]string{
		{"bash", "-c", "rm -rf /"},
		{"sh"},
		{"rm", "-f", "/tmp/x"},
		{"systemctl", "stop", "ceph-osd@7"},
		{"dd", "if=/dev/zero", "of=/dev/sda"},
	} {
		if err := p.Check(argv); err == nil {
			t.Errorf("expected rejected %v, got nil", argv)
		}
	}
}

func TestPolicyAllowsHostTools(t *testing.T) {
	p := legacyAllowlistPolicy()
	allowed := [][]string{
		{"iostat", "-x", "1", "3"},
		{"smartctl", "-a", "/dev/sdc"},
		{"smartctl", "-H", "/dev/sdc"},
		{"dmesg", "-T"},
		{"df", "-h"},
		{"free", "-m"},
		{"uptime"},
		{"lsblk", "-no", "PKNAME", "/dev/mapper/x"},
		{"tail", "-n", "500", "/var/log/ceph/fsid/ceph-osd.7.log"},
		{"grep", "-F", "-c", "error", "/var/log/ceph/fsid/ceph-osd.7.log"},
	}
	for _, argv := range allowed {
		if err := p.Check(argv); err != nil {
			t.Errorf("expected allowed %v, got error: %v", argv, err)
		}
	}
}

func TestPolicyRejectsTailWithUnsafePath(t *testing.T) {
	p := legacyAllowlistPolicy()
	for _, argv := range [][]string{
		{"tail", "-n", "10", "/etc/shadow"},
		{"tail", "-n", "10", "/root/.ssh/id_rsa"},
		{"tail", "-n", "../etc/passwd"},
		{"tail", "-n", "10", "/var/log/ceph/../../../etc/passwd"},
	} {
		if err := p.Check(argv); err == nil {
			t.Errorf("expected rejected %v, got nil", argv)
		}
	}
}

func TestPolicyRejectsGrepPatternStartingWithDash(t *testing.T) {
	p := legacyAllowlistPolicy()
	err := p.Check([]string{"grep", "-F", "--file=/etc/passwd", "x"})
	if err == nil {
		t.Fatal("expected rejected option-like grep pattern")
	}
	if !strings.Contains(err.Error(), "grep") && !strings.Contains(err.Error(), "rejected") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestPolicyRejectsKubectlWrite(t *testing.T) {
	p := legacyAllowlistPolicy()
	allowed := [][]string{
		{"kubectl", "-n", "rook-ceph", "get", "pod", "-l", "app=rook-ceph-tools"},
		{"kubectl", "-n", "rook-ceph", "logs", "rook-ceph-osd-5-abc", "-c", "osd"},
	}
	for _, argv := range allowed {
		if err := p.Check(argv); err != nil {
			t.Errorf("expected allowed %v, got %v", argv, err)
		}
	}
	for _, argv := range [][]string{
		{"kubectl", "delete", "pod", "x"},
		{"kubectl", "apply", "-f", "x.yaml"},
		{"kubectl", "edit", "deploy", "x"},
		{"kubectl", "scale", "deploy", "x", "--replicas=0"},
	} {
		if err := p.Check(argv); err == nil {
			t.Errorf("expected rejected %v, got nil", argv)
		}
	}
}

// —— 只读保证的回归用例 ——
// 这些用例覆盖「外层只读、内层/后续参数才变更」的绕过路径。

func TestPolicyRejectsMutatingInnerExec(t *testing.T) {
	p := legacyAllowlistPolicy()
	rejected := [][]string{
		// kubectl exec 的内层命令必须按独立命令校验
		{"kubectl", "-n", "rook-ceph", "exec", "pod", "--", "rm", "-rf", "/var/lib/rook"},
		{"kubectl", "-n", "rook-ceph", "exec", "pod", "--", "sh", "-c", "ceph osd rm 1"},
		{"kubectl", "-n", "rook-ceph", "exec", "pod", "--", "ceph", "osd", "pool", "set", "x", "size", "1"},
		{"kubectl", "-n", "rook-ceph", "exec", "pod"},
		// crictl / docker exec 同样
		{"crictl", "exec", "abc123", "rm", "-rf", "/tmp/x"},
		{"crictl", "exec", "abc123", "ceph", "osd", "out", "7"},
		{"docker", "exec", "abc123", "sh", "-c", "ceph osd rm 1"},
	}
	for _, argv := range rejected {
		if err := p.Check(argv); err == nil {
			t.Errorf("expected rejected %v, got nil", argv)
		}
	}
}

func TestPolicyAllowsReadOnlyInnerExec(t *testing.T) {
	p := legacyAllowlistPolicy()
	allowed := [][]string{
		{"kubectl", "-n", "rook-ceph", "exec", "pod", "--", "ceph", "-s"},
		{"kubectl", "-n", "rook-ceph", "exec", "pod", "--", "ceph", "osd", "df", "-f", "json"},
		{"crictl", "exec", "abc123", "ceph", "status", "-f", "json"},
		{"docker", "exec", "abc123", "ceph", "pg", "stat"},
	}
	for _, argv := range allowed {
		if err := p.Check(argv); err != nil {
			t.Errorf("expected allowed %v, got %v", argv, err)
		}
	}
}

func TestPolicyRejectsMutatingCephTellArgs(t *testing.T) {
	p := legacyAllowlistPolicy()
	rejected := [][]string{
		// 首词只读、后续参数才变更
		{"ceph", "tell", "osd.0", "perf", "reset"},
		{"ceph", "tell", "osd.0", "injectargs", "--osd_op_timeout=0"},
		{"ceph", "tell", "osd.0", "log", "reset"},
		// 多带参数的只读命令也要拒绝
		{"ceph", "tell", "osd.0", "status", "extra"},
		{"ceph", "tell", "osd.0", "dump_historic_ops", "x"},
	}
	for _, argv := range rejected {
		if err := p.Check(argv); err == nil {
			t.Errorf("expected rejected %v, got nil", argv)
		}
	}
}

func TestPolicyAllowsReadOnlyCephTellShapes(t *testing.T) {
	p := legacyAllowlistPolicy()
	allowed := [][]string{
		{"ceph", "tell", "osd.7", "dump_historic_ops"},
		{"ceph", "tell", "osd.7", "dump_ops_in_flight"},
		{"ceph", "tell", "osd.7", "dump_blocked_ops"},
		{"ceph", "tell", "osd.7", "status"},
		{"ceph", "tell", "osd.7", "version"},
		{"ceph", "tell", "osd.7", "log", "last", "200"},
		{"ceph", "tell", "osd.7", "perf", "dump"},
		{"ceph", "tell", "osd.7", "perf", "schema"},
	}
	for _, argv := range allowed {
		if err := p.Check(argv); err != nil {
			t.Errorf("expected allowed %v, got %v", argv, err)
		}
	}
}

func TestPolicyAllowsNproc(t *testing.T) {
	p := legacyAllowlistPolicy()
	if err := p.Check([]string{"nproc"}); err != nil {
		t.Errorf("nproc 必须放行，否则 host_check 无法按核数归一化负载: %v", err)
	}
}
