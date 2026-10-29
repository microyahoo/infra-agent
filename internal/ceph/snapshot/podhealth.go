package snapshot

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// collectPodHealth 采集 Rook 命名空间的 pod 健康摘要。
//
// 与 rook_pods 工具用同一条 jsonpath：只取需要的字段，
// 避免 -o json 在本集群产生 13 万行输出。
func collectPodHealth(ctx context.Context, c Collector, entry string) string {
	caps := c.Capabilities(ctx, entry)
	if !caps.Kubectl {
		return ""
	}
	ns := caps.Namespace()
	argv := []string{"kubectl", "-n", ns, "get", "pods", "-o",
		"jsonpath={range .items[*]}{.metadata.name}{\"\\t\"}{.spec.nodeName}{\"\\t\"}{.status.phase}{\"\\t\"}" +
			"{.status.containerStatuses[*].ready}{\"\\t\"}{.status.containerStatuses[*].restartCount}{\"\\t\"}" +
			"{.status.containerStatuses[*].state.waiting.reason}{\"\\n\"}{end}"}
	out, err := c.Run(ctx, entry, argv)
	if err != nil {
		return ""
	}
	return summarizePods(out.Stdout)
}

// summarizePods 从 pod 列表输出里挑出异常项，形成一小段摘要。
func summarizePods(table string) string {
	type row struct {
		name     string
		node     string
		phase    string
		notReady bool
		restarts int
		waiting  string
	}
	var rows []row
	total := 0
	for _, line := range strings.Split(table, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		total++
		r := row{name: f[0], node: f[1], phase: f[2]}
		if len(f) > 3 {
			for _, v := range strings.Fields(f[3]) {
				if v == "false" {
					r.notReady = true
				}
			}
		}
		if len(f) > 4 {
			for _, v := range strings.Fields(f[4]) {
				if n, err := strconv.Atoi(v); err == nil {
					r.restarts += n
				}
			}
		}
		if len(f) > 5 {
			for _, v := range strings.Fields(f[5]) {
				if v != "" {
					r.waiting = v
					break
				}
			}
		}
		if r.notReady || r.waiting != "" || r.restarts >= 3 {
			rows = append(rows, r)
		}
	}
	if total == 0 {
		return ""
	}
	if len(rows) == 0 {
		return fmt.Sprintf("%s 下 %d 个 pod 全部就绪，无异常重启", "rook-ceph", total)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].restarts > rows[j].restarts })
	var b strings.Builder
	fmt.Fprintf(&b, "%d 个 pod 中 %d 个异常：\n", total, len(rows))
	for i, r := range rows {
		if i >= 8 {
			b.WriteString("…\n")
			break
		}
		fmt.Fprintf(&b, "- %s @%s：%s，重启 %d 次", r.name, r.node, orDash(r.phase), r.restarts)
		if r.waiting != "" {
			fmt.Fprintf(&b, "，%s", r.waiting)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "未知"
	}
	return s
}
