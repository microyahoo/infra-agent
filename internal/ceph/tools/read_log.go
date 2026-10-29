package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// readLog 的默认与上限行数，上限与 daemon_log 保持一致。
const (
	readLogLinesDefault = 500
	// 带 grep 时默认多读一些，因为绝大多数行会被过滤掉。
	readLogGrepLinesDefault = 1000
	readLogLinesMax         = 5000
)

// readLogPathPrefixes 是允许读取的路径前缀；与 access 策略保持同一口径，
// 其中 /var/lib/rook 覆盖 rook-ceph 及其 logs 子目录。
// 包含 /var/log 全量：排查网络/内核问题时 messages、syslog、kern.log
// 是直接证据，只放 ceph 日志会让这类调查断路。
// 敏感文件（私钥、shadow）由 access 策略单独拒绝。
var readLogPathPrefixes = []string{"/var/log", "/var/lib/rook", "/var/lib/ceph", "/etc/ceph"}

// ReadLogData 是 read_log 的结构化结果。
type ReadLogData struct {
	Path    string `json:"path"`
	Lines   int    `json:"lines"` // 实际请求的 tail 行数（已封顶）
	Grep    string `json:"grep,omitempty"`
	Matched int    `json:"matched"` // 保留的行数
	Content string `json:"content"`
}

type readLogTool struct{ r Runner }

// NewReadLog 构造 read_log 工具。
func NewReadLog(r Runner) Tool { return &readLogTool{r: r} }

func (t *readLogTool) Name() string { return "read_log" }

func (t *readLogTool) Description() string {
	return "读取指定节点上白名单目录内的日志文件。支持读取尾部若干行，或按关键字过滤（过滤在本地进行，不会在远端额外跑 grep）。日志路径只能位于 /var/log/ceph 或 /var/lib/rook 下。"
}

func (t *readLogTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"node":{"type":"string","description":"节点名"},"path":{"type":"string","description":"日志文件绝对路径，必须在 /var/log、/var/lib/rook、/var/lib/ceph 或 /etc/ceph 下"},"tail_lines":{"type":"integer","description":"读取尾部行数，默认 500，最大 5000"},"grep":{"type":"string","description":"可选，只保留包含该子串的行（区分大小写）"}},"required":["node","path"],"additionalProperties":false}`)
}

func (t *readLogTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		Node      string `json:"node"`
		Path      string `json:"path"`
		TailLines int    `json:"tail_lines"`
		Grep      string `json:"grep"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	node := strings.TrimSpace(in.Node)
	if node == "" || strings.TrimSpace(in.Path) == "" {
		return nil, nil, fmt.Errorf("必须提供 node 与 path")
	}
	// 路径会拼进远端命令，拒绝空白与控制字符，避免参数注入到策略之外。
	if strings.ContainsAny(in.Path, " \t\n\r\x00") {
		return nil, nil, fmt.Errorf("path 含有非法字符")
	}
	if !knownNode(t.r, node) {
		return nil, nil, fmt.Errorf("未知节点 %q，可先调用 osd_tree 获取节点名", node)
	}
	if !logPathAllowed(in.Path) {
		return nil, nil, fmt.Errorf("路径 %q 不在白名单内（仅允许 %s 下）", in.Path, strings.Join(readLogPathPrefixes, " 与 "))
	}
	if strings.ContainsAny(in.Grep, "\n\r\x00") {
		return nil, nil, fmt.Errorf("grep 含有非法字符")
	}

	// 未显式给出 tail_lines（或给 0）时：普通读取默认 500 行；
	// 带 grep 时默认 1000 行，过滤在本地完成，多读几行不增加远端 I/O，更容易命中目标。
	lines := in.TailLines
	if lines <= 0 {
		lines = readLogLinesDefault
		if in.Grep != "" {
			lines = readLogGrepLinesDefault
		}
	}
	if lines > readLogLinesMax {
		lines = readLogLinesMax
	}

	argv := []string{"tail", "-n", strconv.Itoa(lines), in.Path}
	out, err := t.r.Run(ctx, node, argv)
	raw := newRaw(node, argv, out)
	if err != nil {
		// 容器化部署（Rook/cephadm）下，daemon 日志往往只在容器 stdout 里，
		// 宿主机上根本没有对应文件。这种失败要把模型引到 daemon_log，
		// 而不是让它反复换路径重试。
		caps := t.r.Capabilities(ctx, node)
		if len(caps.LogDirs) == 0 {
			return nil, raw, fmt.Errorf(
				"读取 %s 失败（%v）：该节点宿主机上没有日志目录，日志很可能在容器内。"+
					"请改用 daemon_log 读取组件日志",
				in.Path, err)
		}
		return nil, raw, fmt.Errorf(
			"读取 %s 失败（%v）：可改用 daemon_log 按组件读取；该节点可用目录：%s",
			in.Path, err, strings.Join(caps.LogDirs, "、"))
	}

	// 过滤在 Go 侧完成：生病的集群不应再承担额外的远端 I/O。
	contentLines := splitLogLines(out.Stdout)
	var kept []string
	if in.Grep == "" {
		kept = contentLines
	} else {
		for _, l := range contentLines {
			if strings.Contains(l, in.Grep) {
				kept = append(kept, l)
			}
		}
	}
	d := &ReadLogData{
		Path:    in.Path,
		Lines:   lines,
		Grep:    in.Grep,
		Matched: len(kept),
		Content: strings.Join(kept, "\n"),
	}

	res := &Result{
		Data:    d,
		Summary: fmt.Sprintf("%s：读取最近 %d 行，保留 %d 行", in.Path, lines, d.Matched),
	}
	if in.Grep != "" {
		res.Summary = fmt.Sprintf("%s：最近 %d 行中匹配 %q 的 %d 行", in.Path, lines, in.Grep, d.Matched)
		if d.Matched == 0 {
			res.Hints = append(res.Hints, fmt.Sprintf("在最近 %d 行中未找到 %q，可增大 tail_lines 或换用更短的关键字", lines, in.Grep))
		}
	}
	if len(contentLines) == 0 {
		res.Summary += "；日志为空"
		res.Hints = append(res.Hints, "日志内容为空，可能文件刚轮转或路径不存在（tail 无输出），可改用 daemon_log 自动定位日志来源")
	}
	return res, raw, nil
}

// logPathAllowed 与 access.Policy 的前缀规则保持一致：拒绝 ..，且必须落在白名单目录内。
func logPathAllowed(p string) bool {
	if strings.Contains(p, "..") {
		return false
	}
	for _, prefix := range readLogPathPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// splitLogLines 按行切分，丢弃末尾空行（tail 输出通常以换行结尾）。
func splitLogLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
