package access

import (
	"context"
	"testing"
)

func TestProbePicksHostCephFirst(t *testing.T) {
	tr := &fakeTransport{
		responses: map[string]fakeResp{
			"ceph --version": {stdout: "ceph version 18.2.4 (e7c...) reef (stable)\n"},
		},
	}
	caps := probeCapabilities(context.Background(), tr)
	if !caps.CephCLI {
		t.Fatal("expected CephCLI true")
	}
	if caps.Backend != "host-ceph" {
		t.Errorf("Backend = %q, want host-ceph", caps.Backend)
	}
	if caps.CephVersion != "18.2.4" {
		t.Errorf("CephVersion = %q", caps.CephVersion)
	}
}

func TestProbeFallsBackToToolbox(t *testing.T) {
	tr := &fakeTransport{
		responses: map[string]fakeResp{
			"ceph --version": {exit: 127, stderr: "command not found"},
			"kubectl get pod -n rook-ceph -l app=rook-ceph-tools": {stdout: "rook-ceph-tools-abc   1/1   Running\n"},
		},
	}
	caps := probeCapabilities(context.Background(), tr)
	if !caps.CephCLI {
		t.Fatal("expected CephCLI true via toolbox")
	}
	if caps.Backend != "toolbox" {
		t.Errorf("Backend = %q, want toolbox", caps.Backend)
	}
}

func TestProbeFallsBackToContainerExec(t *testing.T) {
	tr := &fakeTransport{
		responses: map[string]fakeResp{
			"ceph --version": {exit: 127},
			"kubectl get pod -n rook-ceph -l app=rook-ceph-tools": {exit: 1},
			"crictl ps --name rook-ceph-mon -q":                   {stdout: "abc123\n"},
		},
	}
	caps := probeCapabilities(context.Background(), tr)
	if caps.Backend != "container-exec" {
		t.Errorf("Backend = %q, want container-exec", caps.Backend)
	}
	if caps.ContainerRT != "crictl" {
		t.Errorf("ContainerRT = %q", caps.ContainerRT)
	}
}

func TestProbeFallsBackToLogOnly(t *testing.T) {
	tr := &fakeTransport{
		responses: map[string]fakeResp{
			"ceph --version": {exit: 127},
			"kubectl get pod -n rook-ceph -l app=rook-ceph-tools": {exit: 1},
			"crictl ps --name rook-ceph-mon -q":                   {exit: 1},
			"docker ps --filter name=rook-ceph-mon -q":            {exit: 127},
			"ls -d /var/lib/rook/rook-ceph/logs":                  {stdout: "/var/lib/rook/rook-ceph/logs\n"},
			"ls /var/lib/rook/rook-ceph/logs":                     {stdout: "ceph-osd.7.log\nceph-osd.7.log.1.gz\n"},
		},
	}
	caps := probeCapabilities(context.Background(), tr)
	if caps.Backend != "log-only" {
		t.Errorf("Backend = %q, want log-only", caps.Backend)
	}
	if caps.CephCLI {
		t.Error("CephCLI should be false in log-only mode")
	}
	if len(caps.LogDirs) == 0 {
		t.Error("expected LogDirs populated")
	}
}

func TestProbeParsesVersion(t *testing.T) {
	cases := map[string]string{
		"ceph version 18.2.4 (abc) reef (stable)":     "18.2.4",
		"ceph version 17.2.7 (def) quincy (stable)":   "17.2.7",
		"ceph version 16.2.15 (ghi) pacific (stable)": "16.2.15",
		"garbage": "",
	}
	for in, want := range cases {
		if got := parseCephVersion(in); got != want {
			t.Errorf("parseCephVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProbeHostTools(t *testing.T) {
	tr := &fakeTransport{
		responses: map[string]fakeResp{
			"ceph --version":      {stdout: "ceph version 18.2.4 (x) reef (stable)"},
			"command -v iostat":   {stdout: "/usr/bin/iostat\n"},
			"command -v smartctl": {exit: 1},
			"command -v lsblk":    {stdout: "/usr/bin/lsblk\n"},
		},
	}
	caps := probeCapabilities(context.Background(), tr)
	if !caps.HostTools["iostat"] {
		t.Error("iostat should be available")
	}
	if caps.HostTools["smartctl"] {
		t.Error("smartctl should be unavailable")
	}
	if !caps.HostTools["lsblk"] {
		t.Error("lsblk should be available")
	}
}

// 目录存在但里面只有 cephadm.log 时，不该被当成可用的 ceph 日志来源。
// 这正是 cephadm 部署下的真实形态。
func TestProbeIgnoresDirWithoutCephLogs(t *testing.T) {
	tr := &fakeTransport{
		responses: map[string]fakeResp{
			"ceph --version":      {exit: 127},
			"ls -d /var/log/ceph": {stdout: "/var/log/ceph\n"},
			"ls /var/log/ceph":    {stdout: "cephadm.log\ncephadm.log-20260306.gz\n"},
			"ls -d /var/lib/rook/rook-ceph/log": {
				stdout: "/var/lib/rook/rook-ceph/log\n",
			},
			"ls /var/lib/rook/rook-ceph/log": {
				stdout: "ceph-osd.9.log\nceph-mon.a.log\n",
			},
		},
	}
	caps := probeCapabilities(context.Background(), tr)
	if len(caps.LogDirs) != 1 {
		t.Fatalf("LogDirs = %v, want only the rook log dir", caps.LogDirs)
	}
	if caps.LogDirs[0] != "/var/lib/rook/rook-ceph/log" {
		t.Errorf("LogDirs[0] = %q", caps.LogDirs[0])
	}
}

// Rook 各版本目录名不一致（log / logs），两者都要能识别。
func TestProbeFindsRookSingularLogDir(t *testing.T) {
	tr := &fakeTransport{
		responses: map[string]fakeResp{
			"ceph --version":                    {stdout: "ceph version 16.2.14 (x) pacific (stable)"},
			"ls -d /var/lib/rook/rook-ceph/log": {stdout: "/var/lib/rook/rook-ceph/log\n"},
			"ls /var/lib/rook/rook-ceph/log":    {stdout: "ceph-osd.9.log\n"},
		},
	}
	caps := probeCapabilities(context.Background(), tr)
	found := false
	for _, d := range caps.LogDirs {
		if d == "/var/lib/rook/rook-ceph/log" {
			found = true
		}
	}
	if !found {
		t.Errorf("LogDirs = %v, want to include the singular rook log dir", caps.LogDirs)
	}
}
