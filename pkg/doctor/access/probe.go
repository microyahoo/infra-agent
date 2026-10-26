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

// logDirCandidates 是探测日志目录时的固定候选列表，按优先级排列。
//
// 覆盖三种常见部署的默认位置：
//   - /var/log/ceph                       cephadm / 包安装
//   - /var/lib/rook/rook-ceph/log         Rook 早期版本（单数 log）
//   - /var/lib/rook/rook-ceph/logs        Rook 部分版本（复数 logs）
//
// 候选之外还会做一次目录发现（见 discoverLogDirs），因为不同 Rook 版本的
// 落盘位置并不统一，硬编码候选永远会漏。
var logDirCandidates = []string{
	"/var/log/ceph",
	"/var/lib/rook/rook-ceph/log",
	"/var/lib/rook/rook-ceph/logs",
}

// dirExists 判断远端目录是否存在。
func dirExists(ctx context.Context, tr transport, dir string) bool {
	out, _, code := tr.exec(ctx, []string{"ls", "-d", dir})
	return code == 0 && strings.TrimSpace(out) != ""
}

// hasCephLogs 判断目录下是否真的有 ceph 组件的日志文件。
// 只看目录存在不够：/var/log/ceph 在 cephadm 部署下只有 cephadm.log。
func hasCephLogs(ctx context.Context, tr transport, dir string) bool {
	// 用 shell 通配一次拿到候选文件；policy 允许该形态的 ls。
	out, _, code := tr.exec(ctx, []string{"ls", dir})
	if code != 0 {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "ceph-osd.") ||
			strings.HasPrefix(line, "ceph-mon.") ||
			strings.HasPrefix(line, "ceph-mgr.") ||
			strings.HasPrefix(line, "ceph-mds.") ||
			strings.HasPrefix(line, "ceph-client.") {
			return true
		}
	}
	return false
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
	//
	// 先试固定候选；再确认候选目录里确实有 ceph 日志文件——
	// /var/log/ceph 在 cephadm 环境下可能只放 cephadm.log，
	// 真正的 daemon 日志在别处，此时不应把它当作可用来源。
	for _, dir := range logDirCandidates {
		if !dirExists(ctx, tr, dir) {
			continue
		}
		if hasCephLogs(ctx, tr, dir) {
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
