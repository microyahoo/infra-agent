package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// 日志来源标识，写入 DaemonLogData.Source 与摘要，便于人工核对日志出处。
const (
	logSourceHostLog     = "host-log"
	logSourceRookLog     = "rook-log"
	logSourceKubectlLogs = "kubectl-logs"
	logSourceCrictlLogs  = "crictl-logs"
)

// 两个已知日志目录，与 access.defaultLogDirs 保持一致；probe 只会报告这两个。
const (
	cephLogDir     = "/var/log/ceph"
	rookCephLogDir = "/var/lib/rook/rook-ceph/logs"
)

// 限制 tail 行数，避免一次拉取过多日志冲爆上下文。
const (
	daemonLogLinesDefault = 300
	daemonLogLinesMax     = 5000
)

// allowedDaemons 是可查询日志的 Ceph 守护进程类型。
var allowedDaemons = map[string]bool{
	"mon": true, "mgr": true, "osd": true, "mds": true, "rgw": true,
}

// DaemonLogData 是 daemon_log 的结构化结果。
type DaemonLogData struct {
	Daemon string `json:"daemon"`
	ID     string `json:"id,omitempty"`
	Node   string `json:"node"`
	Path   string `json:"path,omitempty"` // tail 的文件路径 / kubectl pod / crictl 容器 id
	Source string `json:"source"`         // host-log / rook-log / kubectl-logs / crictl-logs
	Lines  int    `json:"lines"`          // 请求的 tail 行数
	Log    string `json:"log"`            // 日志内容
}

type daemonLogTool struct{ r Runner }

// NewDaemonLog 构造 daemon_log 工具。
func NewDaemonLog(r Runner) Tool { return &daemonLogTool{r: r} }

func (t *daemonLogTool) Name() string { return "daemon_log" }

func (t *daemonLogTool) Description() string {
	return "读取某个 Ceph 守护进程（mon/mgr/osd/mds/rgw）最近的日志。Rook 集群的日志位置随版本与部署方式不同，会依次尝试宿主机日志目录、Rook 日志目录、kubectl 与 crictl，并在摘要中说明实际来源。"
}

func (t *daemonLogTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"daemon":{"type":"string","enum":["mon","mgr","osd","mds","rgw"],"description":"守护进程类型"},"id":{"type":"string","description":"守护进程编号，如 osd 的 \"7\"、mon 的 \"a\"；mgr 可省略"},"node":{"type":"string","description":"可选，节点名；默认使用集群入口节点"},"lines":{"type":"integer","description":"读取的行数，默认 300，最多 5000"}},"required":["daemon"],"additionalProperties":false}`)
}

func (t *daemonLogTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		Daemon string `json:"daemon"`
		ID     string `json:"id"`
		Node   string `json:"node"`
		Lines  int    `json:"lines"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	daemon := strings.TrimSpace(in.Daemon)
	if daemon == "" {
		return nil, nil, fmt.Errorf("缺少必填参数 daemon（可选值 mon/mgr/osd/mds/rgw）")
	}
	if !allowedDaemons[daemon] {
		return nil, nil, fmt.Errorf("不支持的守护进程类型 %q（可选值 mon/mgr/osd/mds/rgw）", daemon)
	}
	id := strings.TrimSpace(in.ID)
	if strings.ContainsAny(daemon+id, "\n\r\x00") {
		return nil, nil, fmt.Errorf("daemon/id 含有非法字符")
	}

	lines := in.Lines
	if lines <= 0 {
		lines = daemonLogLinesDefault
	}
	if lines > daemonLogLinesMax {
		lines = daemonLogLinesMax
	}

	node := strings.TrimSpace(in.Node)
	if node == "" {
		node = primaryNode(t.r)
	}
	caps := t.r.Capabilities(ctx, node)

	filename := fmt.Sprintf("ceph-%s.log", daemon)
	if id != "" {
		filename = fmt.Sprintf("ceph-%s.%s.log", daemon, id)
	}

	// 按宿主机日志 → Rook 日志 → kubectl → crictl 的顺序尝试，首个成功即返回。
	var attempts []logAttempt
	if containsDir(caps.LogDirs, cephLogDir) {
		path := cephLogDir + "/" + filename
		attempts = append(attempts, logAttempt{
			source: logSourceHostLog,
			detail: path,
			argv:   []string{"tail", "-n", fmt.Sprint(lines), path},
		})
	}
	if containsDir(caps.LogDirs, rookCephLogDir) {
		path := rookCephLogDir + "/" + filename
		attempts = append(attempts, logAttempt{
			source: logSourceRookLog,
			detail: path,
			argv:   []string{"tail", "-n", fmt.Sprint(lines), path},
		})
	}
	if caps.Kubectl {
		// Rook 的 pod 名带随机后缀（rook-ceph-osd-9-d55f6d445-9jqn2），
		// 不能靠拼名字定位，必须用标签选择器：
		//   osd → ceph-osd-id=<id>
		//   mon/mgr 等 → app=rook-ceph-<daemon>
		sel, describe := rookLogSelector(daemon, id)
		ns := caps.Namespace()
		attempts = append(attempts, logAttempt{
			source: logSourceKubectlLogs,
			detail: describe,
			// Rook 的 osd pod 有 osd / log-collector 两个容器，必须指定容器名。
			argv: []string{"kubectl", "-n", ns, "logs", "-l", sel,
				"-c", rookLogContainer(daemon), "--tail", fmt.Sprint(lines)},
		})
	}
	if caps.ContainerRT == "crictl" {
		attempts = append(attempts, logAttempt{
			source: logSourceCrictlLogs,
			detail: "容器 rook-ceph-" + daemon,
		})
	}

	if len(attempts) == 0 {
		return nil, nil, fmt.Errorf("节点 %s 没有任何可用的日志来源（后端 %s，日志目录 %v）",
			node, caps.Backend, caps.LogDirs)
	}

	var reasons []string
	for _, a := range attempts {
		out, raw, detail, err := t.tryLog(ctx, node, daemon, lines, a)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("%s: %v", a.source, err))
			continue
		}
		d := &DaemonLogData{
			Daemon: daemon,
			ID:     id,
			Node:   node,
			Path:   detail,
			Source: a.source,
			Lines:  lines,
			Log:    out,
		}
		return daemonLogResult(d), raw, nil
	}

	return nil, nil, fmt.Errorf("节点 %s 上未能获取 %s 的日志（后端 %s，日志目录 %v）：%s",
		node, filename, caps.Backend, caps.LogDirs, strings.Join(reasons, "；"))
}

// logAttempt 描述一次日志读取尝试；crictl 需要先解析容器 id，其路径在成功后回填。
type logAttempt struct {
	source string
	detail string // tail 路径 / pod 名 / 容器名前缀，失败信息与结果里展示
	argv   []string
}

// tryLog 执行一次日志读取尝试，返回日志内容、一手材料与实际来源路径。
func (t *daemonLogTool) tryLog(ctx context.Context, node, daemon string, lines int, a logAttempt) (string, *Raw, string, error) {
	if a.source != logSourceCrictlLogs {
		out, err := t.r.Run(ctx, node, a.argv)
		raw := newRaw(node, a.argv, out)
		if err != nil {
			return "", raw, a.detail, err
		}
		return out.Stdout, raw, a.detail, nil
	}

	// crictl：先按名字找到容器 id（取第一行），再按位置参数拉日志。
	name := "rook-ceph-" + daemon
	psArgv := []string{"crictl", "ps", "--name", name, "-q"}
	psOut, psErr := t.r.Run(ctx, node, psArgv)
	if psErr != nil {
		return "", newRaw(node, psArgv, psOut), a.detail, fmt.Errorf("查找容器 %s: %w", name, psErr)
	}
	cid := firstLine(psOut.Stdout)
	if cid == "" {
		return "", newRaw(node, psArgv, psOut), a.detail, fmt.Errorf("未找到名为 %s 的运行中容器", name)
	}
	argv := []string{"crictl", "logs", "--tail", fmt.Sprint(lines), cid}
	out, err := t.r.Run(ctx, node, argv)
	raw := newRaw(node, argv, out)
	if err != nil {
		return "", raw, cid, err
	}
	return out.Stdout, raw, cid, nil
}

// daemonLogResult 组装工具结果，摘要必须写明实际日志来源，方便人工核验。
func daemonLogResult(d *DaemonLogData) *Result {
	subject := "ceph-" + d.Daemon
	if d.ID != "" {
		subject = d.Daemon + "." + d.ID
	}
	res := &Result{
		Data: d,
		Summary: fmt.Sprintf("已从 %s（%s）读取 %s 最近 %d 行日志，来自节点 %s",
			d.Source, d.Path, subject, d.Lines, d.Node),
	}
	if strings.TrimSpace(d.Log) == "" {
		res.Summary += "；日志为空"
		res.Hints = append(res.Hints,
			"日志内容为空，可能是该守护进程刚启动或日志被轮转，可尝试增大 lines 或换用其他守护进程日志")
	}
	return res
}

// containsDir 判断日志目录列表里是否精确包含 dir。
// rookLogContainer 返回 rook 各类 pod 中承载 ceph 日志的容器名。
// osd pod 带一个 log-collector sidecar，不指定容器名 kubectl 会报错。
func rookLogContainer(daemon string) string {
	switch daemon {
	case "osd":
		return "osd"
	case "mon":
		return "mon"
	case "mgr":
		return "mgr"
	case "mds":
		return "mds"
	case "rgw":
		return "rgw"
	}
	return daemon
}

// rookLogSelector 给出按 daemon 类型定位 pod 的标签选择器。
// Rook 的 pod 名带随机后缀，只能靠标签定位。
func rookLogSelector(daemon, id string) (selector, describe string) {
	if daemon == "osd" && id != "" {
		return "ceph-osd-id=" + id, "osd." + id + "（标签 ceph-osd-id=" + id + "）"
	}
	return "app=rook-ceph-" + daemon, daemon + "（标签 app=rook-ceph-" + daemon + "）"
}

func containsDir(dirs []string, dir string) bool {
	for _, d := range dirs {
		if d == dir {
			return true
		}
	}
	return false
}

// firstLine 取输出的第一个非空行并去掉首尾空白。
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			return l
		}
	}
	return ""
}
