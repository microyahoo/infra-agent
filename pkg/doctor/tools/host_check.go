package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// hostCheckLoadWarn 是告警阈值。为保证高核数节点（负载 12.5 也可能真实过载）不被漏报，
// 取较低的固定阈值 10；如需按核数归一化，应在数据里补充 CPU 数后再调整。
const hostCheckLoadWarn = 10.0

// HostCheckData 是 host_check 的结构化结果。
type HostCheckData struct {
	Node   string  `json:"node"`
	Load   string  `json:"load"`   // uptime 原文
	Memory string  `json:"memory"` // free -m 原文
	Disk   string  `json:"disk"`   // df -h 原文
	Load1  float64 `json:"load1"`  // 从 uptime 解析出的 1 分钟负载
}

type hostCheckTool struct{ r Runner }

// NewHostCheck 构造 host_check 工具。
func NewHostCheck(r Runner) Tool { return &hostCheckTool{r: r} }

func (t *hostCheckTool) Name() string { return "host_check" }

func (t *hostCheckTool) Description() string {
	return "在指定节点采集系统负载、内存、文件系统使用率。用于判断 OSD 变慢是宿主机整体资源紧张（而非单块盘）所致。"
}

func (t *hostCheckTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"node":{"type":"string","description":"节点名，来自 osd_tree 或 osd_detail"}},"required":["node"],"additionalProperties":false}`)
}

func (t *hostCheckTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		Node string `json:"node"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	node := strings.TrimSpace(in.Node)
	if node == "" {
		return nil, nil, fmt.Errorf("必须提供 node")
	}
	if !knownNode(t.r, node) {
		return nil, nil, fmt.Errorf("未知节点 %q，可先调用 osd_tree 获取节点名", node)
	}

	d := &HostCheckData{Node: node}
	var raw *Raw
	var errs []string

	// 三条命令互相独立：任一失败只让对应字段留空，不使整个工具失败。
	run := func(argv []string) (string, bool) {
		out, err := t.r.Run(ctx, node, argv)
		if raw == nil {
			raw = newRaw(node, argv, out)
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", argv[0], err))
			return "", false
		}
		return out.Stdout, true
	}

	if s, ok := run([]string{"uptime"}); ok {
		d.Load = strings.TrimSpace(s)
		d.Load1 = parseLoad1(s)
	}
	if s, ok := run([]string{"free", "-m"}); ok {
		d.Memory = strings.TrimSpace(s)
	}
	if s, ok := run([]string{"df", "-h"}); ok {
		d.Disk = strings.TrimSpace(s)
	}

	res := &Result{Data: d}
	switch {
	case d.Load != "":
		res.Summary = fmt.Sprintf("%s：%s", node, d.Load)
	case len(errs) > 0:
		res.Summary = fmt.Sprintf("%s：基础检查数据均未能采集", node)
	default:
		res.Summary = fmt.Sprintf("%s：无输出", node)
	}

	// 只在确实异常时给提示，安静的主机不打扰模型。
	if d.Load1 > hostCheckLoadWarn {
		res.Hints = append(res.Hints, fmt.Sprintf("节点 1 分钟负载 %.2f 偏高，可能是 OSD 变慢的上游原因；可继续用 host_io 确认具体磁盘", d.Load1))
	}
	if fullDisks := dfFullEntries(d.Disk); len(fullDisks) > 0 {
		res.Hints = append(res.Hints, fmt.Sprintf("文件系统使用率已达 100%%：%s，检查是否影响 OSD 数据盘或日志盘", strings.Join(fullDisks, "、")))
	}
	if len(errs) > 0 {
		res.Hints = append(res.Hints, "部分数据源不可用（"+strings.Join(errs, "；")+"）")
	}
	return res, raw, nil
}

// parseLoad1 从 uptime 输出解析 1 分钟负载，例如 "load average: 12.5, 11.0, 9.8"。
func parseLoad1(out string) float64 {
	idx := strings.Index(out, "load average:")
	if idx < 0 {
		return 0
	}
	rest := strings.TrimSpace(out[idx+len("load average:"):])
	first := strings.TrimSpace(strings.Split(rest, ",")[0])
	v, _ := strconv.ParseFloat(first, 64)
	return v
}

// dfFullEntries 返回 df -h 中 Use% 为 100% 的挂载点描述（跳过表头）。
func dfFullEntries(out string) []string {
	var full []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] == "Filesystem" {
			continue
		}
		// 标准 df 输出最后两列是 Use% 与 Mounted on；取倒数第二列判断。
		if fields[len(fields)-2] == "100%" {
			full = append(full, fmt.Sprintf("%s(%s)", fields[len(fields)-1], fields[0]))
		}
	}
	return full
}
