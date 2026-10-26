package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

// iostat 利用率阈值：达到该值视为磁盘打满。
const hostIOUtilWarnPct = 95.0

// HostIOData 是 host_io 的结构化结果。
type HostIOData struct {
	Node           string  `json:"node"`
	Device         string  `json:"device"`
	Iostat         string  `json:"iostat"`                 // 只保留表头与该设备的行
	Dmesg          string  `json:"dmesg"`                  // 只保留提到该设备的行
	Smart          string  `json:"smart,omitempty"`        // smartctl -a 原文
	SmartAvailable bool    `json:"smart_available"`        // 节点上是否有 smartctl
	UtilPercent    float64 `json:"util_percent,omitempty"` // 从 iostat 取到的 %util
	AwaitMS        float64 `json:"await_ms,omitempty"`     // 从 iostat 取到的读写加权 await
}

type hostIOTool struct{ r Runner }

// NewHostIO 构造 host_io 工具。
func NewHostIO(r Runner) Tool { return &hostIOTool{r: r} }

func (t *hostIOTool) Name() string { return "host_io" }

func (t *hostIOTool) Description() string {
	return "在指定节点的指定物理盘上采集 iostat 利用率、与磁盘相关的 dmesg 错误、SMART 健康状态。用于确认 slow_ops / perf_counters 指向的磁盘是否真的有问题。必须给出节点名和盘名（如 sdc）；节点与盘来自 osd_detail 或 osd_tree。"
}

func (t *hostIOTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"node":{"type":"string","description":"节点名，来自 osd_detail 或 osd_tree"},"device":{"type":"string","description":"物理盘名，如 sdc（也接受 /dev/sdc）"}},"required":["node","device"],"additionalProperties":false}`)
}

func (t *hostIOTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		Node   string `json:"node"`
		Device string `json:"device"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	node := strings.TrimSpace(in.Node)
	device := normalizeDevice(in.Device)
	if node == "" || device == "" {
		return nil, nil, fmt.Errorf("必须同时提供 node 与 device（如 node=node-3, device=sdc）")
	}
	// 设备名会拼进路径与命令，拒绝任何控制字符，避免参数注入到策略之外。
	if strings.ContainsAny(device+node, "\n\r\x00 ") {
		return nil, nil, fmt.Errorf("node/device 含有非法字符")
	}
	if !knownNode(t.r, node) {
		return nil, nil, &access.ToolError{
			Kind:    access.KindUnreachable,
			Message: fmt.Sprintf("未知节点 %q，可先调用 osd_tree 获取节点名", node),
		}
	}

	caps := t.r.Capabilities(ctx, node)
	smartAvailable := caps.HostTools["smartctl"]
	d := &HostIOData{
		Node:           node,
		Device:         device,
		SmartAvailable: smartAvailable,
	}
	devPath := "/dev/" + device

	// 三个数据源互相独立，任一失败都不影响其余，保证能降级给出部分结论。
	var raw *Raw
	var errs []string

	if caps.HostTools["iostat"] {
		argv := []string{"iostat", "-x", "1", "3"}
		if out, err := t.r.Run(ctx, node, argv); err != nil {
			errs = append(errs, fmt.Sprintf("iostat: %v", err))
		} else {
			d.Iostat = filterDeviceLines(out.Stdout, device, true)
			d.UtilPercent, d.AwaitMS = parseIostat(d.Iostat)
			if raw == nil {
				raw = newRaw(node, argv, out)
			}
		}
	} else {
		errs = append(errs, "iostat: 节点未安装")
	}

	// dmesg 总是尝试：它不依赖额外工具，且是内核侧 I/O 错误的第一手证据。
	dmesgArgv := []string{"dmesg", "-T"}
	if out, err := t.r.Run(ctx, node, dmesgArgv); err != nil {
		errs = append(errs, fmt.Sprintf("dmesg: %v", err))
	} else {
		d.Dmesg = filterDeviceLines(out.Stdout, device, false)
		if raw == nil {
			raw = newRaw(node, dmesgArgv, out)
		}
	}

	res := &Result{Data: d}
	if smartAvailable {
		argv := []string{"smartctl", "-a", devPath}
		// smartctl 对故障盘的退出码可能是非零，但 stdout 依然有诊断价值，所以保留输出。
		out, err := t.r.Run(ctx, node, argv)
		d.Smart = out.Stdout
		if err != nil {
			errs = append(errs, fmt.Sprintf("smartctl: %v", err))
		}
		if raw == nil {
			raw = newRaw(node, argv, out)
		}
	} else {
		res.Hints = append(res.Hints,
			fmt.Sprintf("节点 %s 没有 smartctl，无法读取 SMART；建议人工在该节点执行 smartctl -a %s 确认硬盘健康状态", node, devPath))
	}

	// 关键：所有数据源都失败时，"未发现异常"是谎言——
	// 那是"没有数据"，不是"没有问题"。必须明确区分，否则模型会把
	// 观测失败当成健康证据，直接排除磁盘方向。
	allFailed := d.Iostat == "" && d.Dmesg == "" && d.Smart == ""
	if allFailed {
		res.Summary = fmt.Sprintf("%s/%s：无法采集任何磁盘数据（iostat/dmesg/smartctl 均失败），不能据此判断磁盘健康", node, device)
		if len(errs) > 0 {
			res.Summary += "；原因：" + clipText(strings.Join(errs, "；"), 200)
		}
	} else {
		res.Summary = fmt.Sprintf("%s/%s：%s", node, device, ioVerdict(d))
		if len(errs) > 0 {
			res.Summary += "（部分数据源失败：" + clipText(strings.Join(errs, "；"), 120) + "）"
		}
	}
	if len(errs) > 0 {
		res.Hints = append(res.Hints, "部分数据源不可用（"+strings.Join(errs, "；")+"）")
	}
	if d.Iostat == "" && caps.HostTools["iostat"] {
		res.Hints = append(res.Hints, fmt.Sprintf("iostat 输出中没有盘 %s，可确认盘名是否正确（如 LVM 盘对应的是其底层物理盘）", device))
	}

	if raw == nil {
		// 三个源都没有可用输出：仍留下节点信息，便于证据链记录「查过哪里」。
		raw = &Raw{Node: node, Command: "iostat/dmesg/smartctl", Stderr: strings.Join(errs, "; ")}
	}
	return res, raw, nil
}

// normalizeDevice 把 /dev/sdc 归一为 sdc；同时去掉常见分区后缀之外的空白。
func normalizeDevice(dev string) string {
	return strings.TrimPrefix(strings.TrimSpace(dev), "/dev/")
}

// filterDeviceLines 只保留提到该设备的行。
// keepHeader 为 true 时额外保留 iostat 的表头行（含 "Device"），让数值可读。
func filterDeviceLines(out, device string, keepHeader bool) string {
	if device == "" {
		return ""
	}
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, device) || (keepHeader && strings.Contains(line, "Device")) {
			kept = append(kept, line)
		}
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n")
}

// parseIostat 从 iostat -x 的行里取 %util 与 await。
// 先按表头定位列号；找不到表头时退化为「第 8 个字段为 await、最后一个是 %util」。
func parseIostat(s string) (util, await float64) {
	awaitIdx, utilIdx := -1, -1
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if awaitIdx < 0 && fields[0] == "Device" {
			for i, f := range fields {
				switch f {
				case "r_await", "await", "w_await":
					awaitIdx = i
				case "%util":
					utilIdx = i
				}
			}
			continue
		}
		if awaitIdx < 0 && !strings.Contains(line, "Device") {
			// 无表头：await 取第 8 列，%util 取最后一列。
			awaitIdx, utilIdx = 7, len(fields)-1
		}
		if awaitIdx < 0 || awaitIdx >= len(fields) || utilIdx < 0 || utilIdx >= len(fields) {
			continue
		}
		if v, err := strconv.ParseFloat(fields[utilIdx], 64); err == nil {
			util = v
		}
		if v, err := strconv.ParseFloat(fields[awaitIdx], 64); err == nil {
			await = v
		}
		return util, await
	}
	return 0, 0
}

// ioVerdict 把三项证据合成一句结论。
func ioVerdict(d *HostIOData) string {
	var parts []string
	if d.UtilPercent >= hostIOUtilWarnPct {
		parts = append(parts, fmt.Sprintf("iostat 显示磁盘利用率 %.1f%%（接近打满）", d.UtilPercent))
	}
	if d.AwaitMS >= 100 {
		parts = append(parts, fmt.Sprintf("平均 await %.1fms 明显偏高", d.AwaitMS))
	}
	if strings.Contains(d.Dmesg, "I/O error") || strings.Contains(d.Dmesg, "Medium Error") ||
		strings.Contains(d.Dmesg, "Sense Key") || strings.Contains(d.Dmesg, "reset") {
		parts = append(parts, "dmesg 有磁盘 I/O 错误")
	}
	if smartFailed(d.Smart) {
		parts = append(parts, "SMART 自检失败")
	}
	if len(parts) == 0 {
		return "未发现明显的磁盘层异常"
	}
	return strings.Join(parts, "；")
}

// smartFailed 判断 smartctl 输出是否报告硬盘健康失败。
func smartFailed(smart string) bool {
	for _, line := range strings.Split(smart, "\n") {
		if strings.Contains(line, "FAILED") ||
			strings.Contains(line, "FAILURE THRESHOLD") ||
			strings.Contains(line, "test result: FAILED") {
			return true
		}
	}
	return false
}

// knownNode 判断节点是否属于当前集群。
func knownNode(r Runner, node string) bool {
	for _, n := range r.Nodes() {
		if n == node {
			return true
		}
	}
	return false
}
