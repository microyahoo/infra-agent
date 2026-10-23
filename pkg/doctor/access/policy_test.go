package access

import (
	"strings"
	"testing"
)

func TestPolicyAllowsReadOnlyCeph(t *testing.T) {
	p := DefaultPolicy()
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
	p := DefaultPolicy()
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
	p := DefaultPolicy()
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
	p := DefaultPolicy()
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
	p := DefaultPolicy()
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
	p := DefaultPolicy()
	err := p.Check([]string{"grep", "-F", "--file=/etc/passwd", "x"})
	if err == nil {
		t.Fatal("expected rejected option-like grep pattern")
	}
	if !strings.Contains(err.Error(), "grep") && !strings.Contains(err.Error(), "rejected") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestPolicyRejectsKubectlWrite(t *testing.T) {
	p := DefaultPolicy()
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
