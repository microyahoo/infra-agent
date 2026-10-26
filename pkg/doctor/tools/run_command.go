package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// runCommandTool 执行模型自行构造的只读命令。
//
// 存在的理由：预设工具只能覆盖"作者想到的"排查路径。真实故障常常需要
// 临时组合的查询（RGW 的 radosgw-admin、mon 的 quorum_status、
// 某个特定路径的日志、ethtool、chronyc……）。没有这个出口，
// 模型遇到预设之外的场景就只能反复跑同样的通用检查。
//
// 安全边界完全由 access.Policy 承担：命令以 argv 形式给出（不经过 shell），
// 执行前逐个参数校验，变更类命令一律拒绝。这个工具本身不做任何放行判断，
// 它只是"把模型的意图翻译成 argv 交给策略层"。
type runCommandTool struct{ r Runner }

// NewRunCommand 构造 run_command 工具。
func NewRunCommand(r Runner) Tool { return &runCommandTool{r: r} }

func (t *runCommandTool) Name() string { return "run_command" }

func (t *runCommandTool) Description() string {
	return "执行一条只读诊断命令。命令必须以 argv 数组的形式给出（每个参数单独一项，不要拼接成字符串）。" +
		"用于预设工具覆盖不到的场景，例如 radosgw-admin、chronyc、ethtool、quorum_status、" +
		"查看某个特定路径的日志等。变更类命令（osd out/rm、pool set、kubectl delete、" +
		"systemctl restart、写文件等）会被策略层拒绝，并返回说明。" +
		"常用参数见 node 字段：不填则用入口节点。"
}

func (t *runCommandTool) Schema() json.RawMessage {
	return json.RawMessage(`{
      "type":"object",
      "properties":{
        "argv":{
          "type":"array","items":{"type":"string"},
          "description":"命令与参数，每项一个元素。例如 [\"ceph\",\"pg\",\"dump\",\"pools\",\"-f\",\"json\"]"
        },
        "node":{
          "type":"string",
          "description":"在哪个节点执行。默认入口节点；查宿主机磁盘/系统日志时需要指定具体节点名"
        },
        "purpose":{
          "type":"string",
          "description":"一句话说明这条命令要验证什么假设，会记录进证据链便于回溯"
        }
      },
      "required":["argv"],
      "additionalProperties":false
    }`)
}

func (t *runCommandTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		Argv    []string `json:"argv"`
		Node    string   `json:"node"`
		Purpose string   `json:"purpose"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	if len(in.Argv) == 0 {
		return nil, nil, fmt.Errorf("argv 不能为空")
	}
	// 常见误用：把整条命令拼成一个字符串。
	// 那会让参数边界丢失，也会让策略层看到一个大字符串而无法逐项判断。
	if len(in.Argv) == 1 && strings.ContainsAny(in.Argv[0], " \t|&;<>$`") {
		return nil, nil, fmt.Errorf(
			"argv 必须是分开的参数数组，不能是整条命令字符串。例如把 %q 拆成 [\"ceph\",\"-s\"]",
			in.Argv[0])
	}

	node := strings.TrimSpace(in.Node)
	if node == "" {
		node = primaryNode(t.r)
	}
	if !knownNode(t.r, node) {
		return nil, nil, fmt.Errorf("未知节点 %q，可先调用 osd_tree 获取节点名", node)
	}

	out, err := t.r.Run(ctx, node, in.Argv)
	raw := newRaw(node, in.Argv, out)
	if err != nil {
		return nil, raw, err
	}

	// 输出通常已经是 JSON 或结构化文本；原样给出前若干行，让模型自己读。
	body := strings.TrimSpace(out.Stdout)
	lines := strings.Split(body, "\n")
	const maxLines = 120
	truncated := false
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}
	text := strings.Join(lines, "\n")

	res := &Result{
		Data: map[string]any{
			"argv":      in.Argv,
			"node":      node,
			"purpose":   in.Purpose,
			"output":    text,
			"lines":     len(lines),
			"truncated": truncated,
		},
	}

	cmd := strings.Join(in.Argv, " ")
	if out.Stdout == "" && out.Stderr != "" {
		res.Summary = fmt.Sprintf("%s：命令无输出，stderr: %s", cmd, clipText(out.Stderr, 200))
		return res, raw, nil
	}
	first := ""
	if len(lines) > 0 {
		first = clipText(lines[0], 160)
	}
	res.Summary = fmt.Sprintf("%s @%s：%d 行输出，首行 %q", cmd, node, len(lines), first)
	if truncated {
		res.Summary += "（已截断）"
	}
	return res, raw, nil
}

// clipText 按 rune 安全截断，用于把输出压进一行摘要。
func clipText(s string, limit int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
