package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// PodInfo 是 Rook 命名空间里一个 pod 的关键状态。
type PodInfo struct {
	Name       string `json:"name"`
	Daemon     string `json:"daemon"` // osd / mon / mgr / mds / rgw / operator / 其它
	Phase      string `json:"phase"`
	Ready      bool   `json:"ready"`
	Node       string `json:"node,omitempty"`
	Restarts   int    `json:"restarts"`
	Waiting    string `json:"waiting,omitempty"` // CrashLoopBackOff / ImagePullBackOff ...
	LastExit   int    `json:"last_exit_code,omitempty"`
	LastReason string `json:"last_reason,omitempty"` // Error / OOMKilled / Completed
}

// RookPodsData 是 rook_pods 的结构化结果。
type RookPodsData struct {
	Namespace string    `json:"namespace"`
	Pods      []PodInfo `json:"pods"`
	Events    []string  `json:"events,omitempty"`
	Describe  string    `json:"describe,omitempty"`
}

// podJSONPath 只取诊断需要的字段。
//
// 不用 -o json：本集群的 pod 列表有 13 万行（annotation 里嵌了大量 JSON），
// 会被输出上限截断。制表符用 kubectl 的 {"\t"} 语法，避免在 argv 里
// 塞真实控制字符（策略层会拒绝）。
const podJSONPath = `{range .items[*]}{.metadata.name}{"\t"}{.spec.nodeName}{"\t"}{.status.phase}{"\t"}` +
	`{.status.containerStatuses[*].ready}{"\t"}{.status.containerStatuses[*].restartCount}{"\t"}` +
	`{.status.containerStatuses[*].state.waiting.reason}{"\t"}` +
	`{.status.containerStatuses[*].lastState.terminated.exitCode}{"\n"}{end}`

type rookPodsTool struct{ r Runner }

// NewRookPods 构造 rook_pods 工具。
func NewRookPods(r Runner) Tool { return &rookPodsTool{r: r} }

func (t *rookPodsTool) Name() string { return "rook_pods" }

func (t *rookPodsTool) Description() string {
	return "查看 Rook 命名空间里各 Ceph 组件 pod 的状态：是否 Running、重启次数、CrashLoopBackOff、" +
		"最后一次退出码、所在节点。Rook 部署下，OSD/mon/mgr 的进程问题首先体现在 pod 状态上——" +
		"当多个 OSD 同时异常或 daemon 反复掉线时，先看这里。可选：指定 pod 名查看详情，或列出近期事件。"
}

func (t *rookPodsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
      "type":"object",
      "properties":{
        "pod":{"type":"string","description":"可选，只看这个 pod 的详细信息（describe）"},
        "events":{"type":"boolean","description":"可选，同时列出该命名空间的近期事件"}
      },
      "additionalProperties":false
    }`)
}

func (t *rookPodsTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		Pod    string `json:"pod"`
		Events bool   `json:"events"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &in)
	}

	node := primaryNode(t.r)
	caps := t.r.Capabilities(ctx, node)
	if !caps.Kubectl {
		return nil, nil, fmt.Errorf("此环境没有可用的 kubectl（后端 %s），看不到 pod 状态；"+
			"可改用 daemon_log / read_log 从日志分析", caps.Backend)
	}
	ns := caps.Namespace()
	d := &RookPodsData{Namespace: ns}

	var rawOut *Raw

	if in.Pod != "" {
		argv := []string{"kubectl", "-n", ns, "describe", "pod", in.Pod}
		out, err := t.r.Run(ctx, node, argv)
		rawOut = newRaw(node, argv, out)
		if err != nil {
			return nil, rawOut, err
		}
		d.Describe = strings.TrimSpace(out.Stdout)
		res := &Result{
			Data:    d,
			Summary: fmt.Sprintf("%s/%s 详情已获取（%d 行）", ns, in.Pod, len(strings.Split(d.Describe, "\n"))),
		}
		res.Hints = describeHints(d.Describe)
		return res, rawOut, nil
	}

	// 只取需要的字段。kubectl get pods -o json 在本集群有 13 万行
	//（pod annotation 里嵌了大量 JSON），会被输出上限截断导致解析失败。
	argv := []string{"kubectl", "-n", ns, "get", "pods", "-o", "jsonpath=" + podJSONPath}
	out, err := t.r.Run(ctx, node, argv)
	rawOut = newRaw(node, argv, out)
	if err != nil {
		return nil, rawOut, err
	}
	pods := parsePodTable(out.Stdout)
	if len(pods) == 0 {
		return nil, rawOut, fmt.Errorf("未解析到任何 pod（输出前 200 字节：%s）", clipText(out.Stdout, 200))
	}
	d.Pods = pods

	if in.Events {
		eargv := []string{"kubectl", "-n", ns, "get", "events", "--sort-by=.lastTimestamp"}
		if eout, eerr := t.r.Run(ctx, node, eargv); eerr == nil {
			d.Events = interestingEvents(eout.Stdout)
		}
	}

	res := &Result{Data: d}
	if len(pods) == 0 {
		res.Summary = fmt.Sprintf("%s 下没有 pod", ns)
		return res, rawOut, nil
	}

	notReady, crash, restarted := 0, 0, 0
	var problemPods []string
	for _, p := range pods {
		if !p.Ready {
			notReady++
		}
		if p.Waiting == "CrashLoopBackOff" {
			crash++
		}
		if p.Restarts >= 3 {
			restarted++
		}
		if !p.Ready || p.Waiting != "" || p.Restarts >= 3 {
			problemPods = append(problemPods, fmt.Sprintf("%s（%s，重启 %d 次%s）",
				p.Name, statusOf(p), p.Restarts, exitSuffix(p)))
		}
	}

	res.Summary = fmt.Sprintf("%s：%d 个 pod，%d 个未就绪，%d 个 CrashLoopBackOff，%d 个重启≥3 次",
		ns, len(pods), notReady, crash, restarted)
	if len(problemPods) > 0 {
		sort.Strings(problemPods)
		if len(problemPods) > 8 {
			problemPods = append(problemPods[:8], "…")
		}
		res.Summary += "；异常：" + strings.Join(problemPods, "、")
		res.Hints = []string{
			"用 rook_pods(pod=\"<名称>\") 看具体 pod 的容器状态、事件与上次退出原因",
			"若多个 pod 在同一节点上同时异常，先确认该节点本身是否健康（host_check）",
			"有了 pod 名后可用 daemon_log 读该组件的日志",
		}
	}
	if len(d.Events) > 0 {
		res.Hints = append(res.Hints, "近期事件见 events 字段")
	}
	return res, rawOut, nil
}

// describeHints 从 describe 输出里提取线索。
func describeHints(desc string) []string {
	var hints []string
	l := strings.ToLower(desc)
	if strings.Contains(l, "crashloopbackoff") || strings.Contains(l, "back-off restarting") {
		hints = append(hints, "容器在反复重启：看 describe 里的 Last State（退出码/原因）与 Events")
	}
	if strings.Contains(l, "liveness probe") {
		hints = append(hints, "liveness 探针失败：大盘 OSD 启动慢时探针可能在就绪前就杀掉容器，值得核对探针超时与 OSD 启动耗时")
	}
	if strings.Contains(l, "oomkilled") {
		hints = append(hints, "容器被 OOMKilled：内存限制偏小或内存泄漏")
	}
	if strings.Contains(l, "failedmount") || strings.Contains(l, "unable to attach") {
		hints = append(hints, "存储挂载失败：可能磁盘未就绪、设备被占用或 ceph-volume 状态异常")
	}
	return hints
}

// interestingEvents 从事件列表里挑出与故障相关的行。
func interestingEvents(out string) []string {
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "warning") || strings.Contains(low, "failed") ||
			strings.Contains(low, "backoff") || strings.Contains(low, "kill") ||
			strings.Contains(low, "unhealthy") {
			kept = append(kept, strings.TrimSpace(line))
		}
	}
	const max = 20
	if len(kept) > max {
		kept = kept[len(kept)-max:]
	}
	return kept
}

// parsePodTable 解析我们自定义的 jsonpath 输出。
//
// 每行：name \t node \t phase \t ready列表 \t restarts列表 \t waiting列表 \t exitcode列表
// 容器列表以空格分隔（jsonpath 对数组的默认分隔符）。
func parsePodTable(out string) []PodInfo {
	var pods []PodInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		p := PodInfo{
			Name:   strings.TrimSpace(f[0]),
			Node:   strings.TrimSpace(f[1]),
			Phase:  strings.TrimSpace(f[2]),
			Daemon: daemonOf(f[0]),
		}
		if len(f) > 3 {
			p.Ready = allTrue(strings.Fields(f[3]))
		} else {
			p.Ready = p.Phase == "Running"
		}
		if len(f) > 4 {
			p.Restarts = sumInts(strings.Fields(f[4]))
		}
		if len(f) > 5 {
			for _, r := range strings.Fields(f[5]) {
				if r != "" && r != "<none>" {
					p.Waiting = r
					break
				}
			}
		}
		if len(f) > 6 {
			for _, e := range strings.Fields(f[6]) {
				if v, err := strconv.Atoi(e); err == nil && v != 0 {
					p.LastExit = v
					break
				}
			}
		}
		pods = append(pods, p)
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	return pods
}

// allTrue 判断 ready 列表是否全部为 true。
func allTrue(vals []string) bool {
	if len(vals) == 0 {
		return false
	}
	for _, v := range vals {
		if v != "true" {
			return false
		}
	}
	return true
}

// sumInts 求整数列表之和。
func sumInts(vals []string) int {
	sum := 0
	for _, v := range vals {
		if n, err := strconv.Atoi(v); err == nil {
			sum += n
		}
	}
	return sum
}

// daemonOf 从 pod 名推断组件类型。
func daemonOf(name string) string {
	switch {
	case strings.Contains(name, "-osd-"):
		return "osd"
	case strings.Contains(name, "-mon-"):
		return "mon"
	case strings.Contains(name, "-mgr-"):
		return "mgr"
	case strings.Contains(name, "-mds-"):
		return "mds"
	case strings.Contains(name, "rgw"):
		return "rgw"
	case strings.Contains(name, "tools"):
		return "tools"
	case strings.Contains(name, "operator"):
		return "operator"
	}
	return "other"
}

func statusOf(p PodInfo) string {
	if p.Waiting != "" {
		return p.Waiting
	}
	if p.Phase != "" {
		return p.Phase
	}
	return "unknown"
}

func exitSuffix(p PodInfo) string {
	var parts []string
	if p.LastExit != 0 {
		parts = append(parts, fmt.Sprintf("退出码 %d", p.LastExit))
	}
	if p.LastReason != "" {
		parts = append(parts, p.LastReason)
	}
	if len(parts) == 0 {
		return ""
	}
	return "，" + strings.Join(parts, "/")
}
