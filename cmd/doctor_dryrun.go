package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/microyahoo/infra-agent/internal/session"
)

// runDoctorDryRun 只做能力探测与拓扑发现，打印摘要后返回，不调用模型。
func runDoctorDryRun(ctx context.Context, sess *session.Session) error {
	cluster := sess.Cluster()
	nodes := cluster.Nodes()
	if len(nodes) == 0 {
		return fmt.Errorf("未能确定任何节点")
	}
	primary := nodes[0]

	fmt.Fprintf(os.Stderr, "🔎 探测节点能力 ...\n")
	caps := cluster.Capabilities(ctx, primary)
	fmt.Fprintf(os.Stderr, "  → 后端 %s，ceph CLI %s\n", orUnknown(caps.Backend), yesNo(caps.CephCLI))

	fmt.Fprintf(os.Stderr, "🔎 发现集群拓扑 ...\n")
	topo, topoErr := cluster.Topology(ctx)
	if topoErr != nil {
		fmt.Fprintf(os.Stderr, "  → 拓扑发现失败：%v\n", topoErr)
	} else if topo != nil {
		fmt.Fprintf(os.Stderr, "  → %d 个节点，%d 个 OSD\n", len(topo.Nodes), len(topo.OSDs))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "SSH 接入验证（dry-run）\n")
	fmt.Fprintf(&b, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Fprintf(&b, "入口节点: %s\n", primary)
	fmt.Fprintf(&b, "诊断后端: %s\n", orUnknown(caps.Backend))
	fmt.Fprintf(&b, "ceph CLI: %s\n", yesNo(caps.CephCLI))
	if caps.CephVersion != "" {
		fmt.Fprintf(&b, "ceph 版本: %s\n", caps.CephVersion)
	}
	fmt.Fprintf(&b, "kubectl: %s\n", yesNo(caps.Kubectl))
	if caps.ContainerRT != "" && caps.ContainerRT != "none" {
		fmt.Fprintf(&b, "容器运行时: %s\n", caps.ContainerRT)
	}
	fmt.Fprintf(&b, "日志目录: %s\n", listOrNone(caps.LogDirs))
	toolNames := make([]string, 0, len(caps.HostTools))
	for name, ok := range caps.HostTools {
		if ok {
			toolNames = append(toolNames, name)
		}
	}
	sort.Strings(toolNames)
	fmt.Fprintf(&b, "宿主机工具: %s\n", listOrNone(toolNames))

	if topoErr != nil {
		fmt.Fprintf(&b, "集群拓扑: 发现失败：%v\n", topoErr)
	} else {
		fmt.Fprintf(&b, "集群拓扑: fsid %s，节点 %d，OSD %d\n",
			orUnknown(topo.FSID), len(topo.Nodes), len(topo.OSDs))
		for _, n := range topo.Nodes {
			fmt.Fprintf(&b, "  - %s（主机 %s，OSD %d）\n",
				n.Name, orUnknown(n.Host), len(n.OSDIDs))
		}
	}
	fmt.Fprintf(&b, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Fprintf(&b, "dry-run 结束：SSH 与探测已完成，未调用模型\n")

	fmt.Fprint(os.Stdout, b.String())
	return nil
}

// orUnknown 把空字符串显示为 unknown。
func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

// yesNo 把布尔值转为中文可读文本。
func yesNo(ok bool) string {
	if ok {
		return "可用"
	}
	return "不可用"
}

// listOrNone 把列表拼接为逗号分隔文本，空列表显示为「无」。
func listOrNone(items []string) string {
	if len(items) == 0 {
		return "无"
	}
	return strings.Join(items, ", ")
}
