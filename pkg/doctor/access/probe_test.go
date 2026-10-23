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
