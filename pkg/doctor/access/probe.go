package access

import (
	"context"
	"regexp"
	"strings"
)

// Capabilities 描述某节点实际具备的能力。
// 探测结果是后续三件事的依据：注册哪些工具、告诉模型能做什么/不能做什么、
// 以及报告声明结论所依赖的数据来源。降级到 log-only 是合法结果而非失败。
type Capabilities struct {
	CephCLI     bool
	CephVersion string
	Backend     string // host-ceph / toolbox / container-exec / log-only
	Kubectl     bool
	ContainerRT string // crictl / docker / none
	LogDirs     []string
	HostTools   map[string]bool
}

// transport 是探测需要的执行能力，便于测试注入。
type transport interface {
	exec(ctx context.Context, argv []string) (string, string, int)
}

var versionRe = regexp.MustCompile(`ceph version (\d+\.\d+\.\d+)`)

// parseCephVersion 从 "ceph version 18.2.4 (commit) reef (stable)" 中取出 x.y.z。
func parseCephVersion(s string) string {
	m := versionRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// defaultLogDirs 是探测日志目录时的固定候选列表。
var defaultLogDirs = []string{
	"/var/log/ceph",
	"/var/lib/rook/rook-ceph/logs",
}

// probeCapabilities 按 host-ceph → toolbox → container-exec → log-only 顺序探测。
// 所有命令都必须能通过 Policy 校验，真实传输会在执行前拦截未登记的 argv。
func probeCapabilities(ctx context.Context, tr transport) Capabilities {
	return probeCapabilitiesWithProgress(ctx, tr, nil)
}

// probeCapabilitiesWithProgress 同 probeCapabilities，另在关键步骤回报进度。
func probeCapabilitiesWithProgress(ctx context.Context, tr transport, progress func(string)) Capabilities {
	report := func(msg string) {
		if progress != nil {
			progress(msg)
		}
	}
	caps := Capabilities{HostTools: map[string]bool{}}

	report("检查宿主机 ceph CLI")
	// 1. 宿主机 ceph 客户端
	if out, _, code := tr.exec(ctx, []string{"ceph", "--version"}); code == 0 {
		if v := parseCephVersion(out); v != "" {
			caps.CephCLI = true
			caps.CephVersion = v
			caps.Backend = "host-ceph"
		}
	}

	report("检查 kubectl / rook toolbox")
	// 2. kubectl + toolbox pod
	if out, _, code := tr.exec(ctx, []string{"kubectl", "get", "pod", "-n", "rook-ceph", "-l", "app=rook-ceph-tools"}); code == 0 {
		caps.Kubectl = true
		if !caps.CephCLI && strings.Contains(out, "Running") {
			caps.CephCLI = true
			caps.Backend = "toolbox"
		}
	}

	// 3. 容器运行时
	report("检查容器运行时（crictl / docker）")
	if !caps.CephCLI {
		if out, _, code := tr.exec(ctx, []string{"crictl", "ps", "--name", "rook-ceph-mon", "-q"}); code == 0 && strings.TrimSpace(out) != "" {
			caps.CephCLI = true
			caps.Backend = "container-exec"
			caps.ContainerRT = "crictl"
		} else if out, _, code := tr.exec(ctx, []string{"docker", "ps", "--filter", "name=rook-ceph-mon", "-q"}); code == 0 && strings.TrimSpace(out) != "" {
			caps.CephCLI = true
			caps.Backend = "container-exec"
			caps.ContainerRT = "docker"
		}
	}

	report("检查日志目录")
	// 4. 日志目录（总是探测，log-only 时是唯一数据源）
	for _, dir := range defaultLogDirs {
		if out, _, code := tr.exec(ctx, []string{"ls", "-d", dir}); code == 0 && strings.TrimSpace(out) != "" {
			caps.LogDirs = append(caps.LogDirs, dir)
		}
	}
	if caps.Backend == "" {
		caps.Backend = "log-only"
	}
	if caps.ContainerRT == "" {
		caps.ContainerRT = "none"
	}

	report("检查宿主机工具（iostat / smartctl / lsblk / dmesg）")
	// 5. 宿主机工具
	for _, tool := range []string{"iostat", "smartctl", "lsblk", "dmesg"} {
		_, _, code := tr.exec(ctx, []string{"command", "-v", tool})
		caps.HostTools[tool] = code == 0
	}

	return caps
}
