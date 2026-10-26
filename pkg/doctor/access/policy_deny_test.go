package access

import (
	"testing"
)

// 核心保证：任何会改变集群或系统状态的命令都必须被拒绝。
func TestDenyPolicyRejectsMutations(t *testing.T) {
	p := DefaultDenyPolicy()
	rejected := [][]string{
		// ceph 变更
		{"ceph", "osd", "out", "7"},
		{"ceph", "osd", "in", "7"},
		{"ceph", "osd", "rm", "7"},
		{"ceph", "osd", "crush", "remove", "osd.7"},
		{"ceph", "osd", "pool", "create", "x", "64"},
		{"ceph", "osd", "pool", "set", "x", "size", "3"},
		{"ceph", "osd", "pool", "delete", "x"},
		{"ceph", "osd", "set", "noout"},
		{"ceph", "osd", "purge", "7"},
		{"ceph", "config", "set", "global", "x", "y"},
		{"ceph", "pg", "repair", "2.4a"},
		{"ceph", "pg", "scrub", "2.4a"},
		{"ceph", "mon", "remove", "a"},
		{"ceph", "mgr", "fail", "x"},
		// tell 路径的变更
		{"ceph", "tell", "osd.7", "injectargs", "--osd_op_timeout=0"},
		{"ceph", "tell", "osd.7", "perf", "reset"},
		{"ceph", "tell", "osd.7", "config", "set", "x", "y"},
		{"ceph", "tell", "osd.7", "bench", "10", "write"},
		// k8s 变更
		{"kubectl", "delete", "pod", "x"},
		{"kubectl", "apply", "-f", "x.yaml"},
		{"kubectl", "scale", "deploy", "x", "--replicas=0"},
		{"kubectl", "drain", "node1"},
		{"kubectl", "cordon", "node1"},
		// 容器变更
		{"docker", "rm", "abc"},
		{"crictl", "stop", "abc"},
		// 主机变更
		{"systemctl", "restart", "ceph-osd@7"},
		{"reboot"},
		{"mkfs.ext4", "/dev/sdb"},
		{"dd", "if=/dev/zero", "of=/dev/sda"},
		// 未登记的程序
		{"rm", "-rf", "/var/lib/rook"},
		{"bash", "-c", "ceph osd rm 1"},
		{"sh", "-c", "rm -rf /"},
		{"python3", "-c", "import os; os.remove('/x')"},
	}
	for _, argv := range rejected {
		if err := p.Check(argv); err == nil {
			t.Errorf("LEAK: mutating command allowed: %v", argv)
		}
	}
}

// 常见的只读诊断命令必须全部放行——这是"自主定位"的前提。
func TestDenyPolicyAllowsReadOnlyExploration(t *testing.T) {
	p := DefaultDenyPolicy()
	allowed := [][]string{
		// 全景
		{"ceph", "-s"}, {"ceph", "status", "-f", "json"},
		{"ceph", "health", "detail", "-f", "json"},
		{"ceph", "df", "detail", "-f", "json"},
		{"ceph", "osd", "tree", "-f", "json"},
		{"ceph", "osd", "df", "-f", "json"},
		{"ceph", "osd", "metadata", "7", "-f", "json"},
		{"ceph", "pg", "dump_stuck"},
		{"ceph", "pg", "2.4a", "query"},
		{"ceph", "versions"},
		{"ceph", "log", "last", "100"},
		{"ceph", "config", "get", "osd", "log_file"},
		// 以前查不了的，现在必须能查
		{"ceph", "osd", "perf", "-f", "json"},
		{"ceph", "pg", "dump", "pools", "-f", "json"},
		{"ceph", "mon", "stat", "-f", "json"},
		{"ceph", "osd", "lspools"},
		{"ceph", "fs", "status"},
		{"ceph", "fs", "ls"},
		{"ceph", "osd", "blocklist", "ls"},
		{"ceph", "auth", "list"},
		{"ceph", "quorum_status"},
		{"radosgw-admin", "user", "list"},
		{"radosgw-admin", "bucket", "list"},
		{"rbd", "ls", "rbd"},
		{"ceph", "tell", "osd.7", "dump_historic_ops"},
		{"ceph", "tell", "osd.7", "perf", "dump"},
		{"ceph", "tell", "mon.a", "mon_status"},
		// 主机层
		{"iostat", "-x", "1", "5"},
		{"mpstat", "-P", "ALL", "1", "3"},
		{"vmstat", "1", "3"},
		{"pidstat", "-d", "1", "3"},
		{"smartctl", "-a", "/dev/sdb"},
		{"nvme", "smart-log", "/dev/nvme0n1"},
		{"dmesg", "-T"},
		{"journalctl", "-u", "ceph-osd@7", "--no-pager", "-n", "100"},
		{"ss", "-s"},
		{"ip", "-s", "link"},
		{"ethtool", "eth0"},
		{"ping", "-c", "3", "10.3.14.49"},
		// 文件层
		{"ls", "/var/lib/rook/rook-ceph/log"},
		{"tail", "-n", "200", "/var/lib/rook/rook-ceph/log/ceph-osd.9.log"},
		{"grep", "-c", "error", "/var/lib/rook/rook-ceph/log/ceph-osd.9.log"},
		{"find", "/var/lib/rook", "-name", "*.log", "-type", "f"},
		{"du", "-sh", "/var/lib/rook"},
		{"df", "-h"},
		{"lsblk", "-o", "NAME,SIZE,TYPE,MOUNTPOINT"},
		// k8s 侧
		{"kubectl", "-n", "rook-ceph", "get", "pods", "-o", "wide"},
		{"kubectl", "-n", "rook-ceph", "describe", "pod", "x"},
		{"kubectl", "-n", "rook-ceph", "logs", "-l", "ceph-osd-id=9", "-c", "osd"},
		{"kubectl", "-n", "rook-ceph", "get", "events"},
		{"kubectl", "get", "ns"},
	}
	for _, argv := range allowed {
		if err := p.Check(argv); err != nil {
			t.Errorf("read-only command rejected: %v (%v)", argv, err)
		}
	}
}

// exec 的内层命令同样受约束。
func TestDenyPolicyRejectsMutatingInnerExec(t *testing.T) {
	p := DefaultDenyPolicy()
	rejected := [][]string{
		{"kubectl", "-n", "rook-ceph", "exec", "pod", "--", "rm", "-rf", "/var/lib/rook"},
		{"kubectl", "-n", "rook-ceph", "exec", "pod", "--", "ceph", "osd", "out", "7"},
		{"kubectl", "-n", "rook-ceph", "exec", "pod", "--", "sh", "-c", "ceph osd rm 1"},
		{"crictl", "exec", "abc", "rm", "-rf", "/x"},
		{"docker", "exec", "abc", "ceph", "osd", "rm", "7"},
	}
	for _, argv := range rejected {
		if err := p.Check(argv); err == nil {
			t.Errorf("LEAK: mutating inner exec allowed: %v", argv)
		}
	}

	allowed := [][]string{
		{"kubectl", "-n", "rook-ceph", "exec", "pod", "--", "ceph", "-s"},
		{"crictl", "exec", "abc", "ceph", "status", "-f", "json"},
	}
	for _, argv := range allowed {
		if err := p.Check(argv); err != nil {
			t.Errorf("read-only inner exec rejected: %v (%v)", argv, err)
		}
	}
}

// 路径穿越永远拒绝。
func TestDenyPolicyRejectsTraversal(t *testing.T) {
	p := DefaultDenyPolicy()
	for _, argv := range [][]string{
		{"cat", "/root/.ssh/id_rsa"},
		{"tail", "-n", "10", "/etc/shadow"},
		{"ls", "/var/log/../../etc"},
	} {
		if err := p.Check(argv); err == nil {
			t.Errorf("LEAK: sensitive path allowed: %v", argv)
		}
	}
}

// "osd settings" 这类词不应被 "osd set" 误伤。
func TestDenyPolicyWordBoundary(t *testing.T) {
	p := DefaultDenyPolicy()
	if err := p.Check([]string{"ceph", "osd", "settings", "-f", "json"}); err != nil {
		t.Errorf("word boundary false positive: %v", err)
	}
	// 但真正的 osd set 必须拒绝
	if err := p.Check([]string{"ceph", "osd", "set", "noout"}); err == nil {
		t.Error("osd set should be rejected")
	}
}
