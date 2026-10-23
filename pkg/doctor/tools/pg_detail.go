package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// StuckPG 一个卡住的 PG。
type StuckPG struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// PGDetailData 是 pg_detail 的结构化结果。
type PGDetailData struct {
	Stat       string    `json:"stat"`
	StuckPGs   []StuckPG `json:"stuck_pgs,omitempty"`
	StuckCount int       `json:"stuck_count"`
}

type pgDetailTool struct{ r Runner }

// NewPGDetail 构造 pg_detail 工具。
func NewPGDetail(r Runner) Tool { return &pgDetailTool{r: r} }

func (t *pgDetailTool) Name() string { return "pg_detail" }

func (t *pgDetailTool) Description() string {
	return "查看 PG 汇总状态与卡住（stuck）的 PG 列表。用于分析 degraded / peering / stale 等异常 PG 的规模与具体编号。"
}

func (t *pgDetailTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"state":{"type":"string","description":"可选，只看特定状态，如 peering / degraded / stale"}},"additionalProperties":false}`)
}

func (t *pgDetailTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	node := primaryNode(t.r)
	caps := t.r.Capabilities(ctx, node)
	if !caps.CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI（后端 %s），无法执行 ceph pg；可改用 daemon_log 查看 mon/osd 日志", caps.Backend)
	}
	var in struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(args, &in)
	state := strings.TrimSpace(in.State)

	statArgv := []string{"ceph", "pg", "stat"}
	statOut, err := t.r.Run(ctx, node, statArgv)
	if err != nil {
		return nil, nil, err
	}

	// dump_stuck 在病态集群上经常超时或卡住，失败时仍用 pg stat 的汇总产出结果。
	dumpArgs := []string{"ceph", "pg", "dump_stuck"}
	if state != "" {
		dumpArgs = append(dumpArgs, state)
	}
	dumpOut, derr := t.r.Run(ctx, node, dumpArgs)

	d := &PGDetailData{Stat: strings.TrimSpace(statOut.Stdout)}
	raw := newRaw(node, statArgv, statOut)
	var stuckErr string
	if derr != nil {
		stuckErr = fmt.Sprintf("ceph pg dump_stuck 执行失败（%v），只能提供汇总信息", derr)
	} else {
		raw = newRaw(node, dumpArgs, dumpOut)
		for _, line := range strings.Split(dumpOut.Stdout, "\n") {
			fields := strings.Fields(line)
			// 跳过表头与日志行：只有形如 2.4a 的第一列才算 PG。
			if len(fields) < 2 || !looksLikePGID(fields[0]) {
				continue
			}
			d.StuckPGs = append(d.StuckPGs, StuckPG{ID: fields[0], State: fields[1]})
		}
		d.StuckCount = len(d.StuckPGs)
	}

	res := &Result{
		Data:    d,
		Summary: fmt.Sprintf("PG 汇总：%s；卡住（%s）%d 个", d.Stat, stuckStateLabel(state), d.StuckCount),
	}
	if d.StuckCount > 0 {
		var ids []string
		for i, p := range d.StuckPGs {
			if i >= 5 {
				ids = append(ids, "...")
				break
			}
			ids = append(ids, p.ID)
		}
		res.Hints = append(res.Hints,
			"卡住的 PG："+strings.Join(ids, "、")+"。调用 osd_detail 查看其主 OSD 状态，或 daemon_log 查看该 OSD 的恢复日志")
	}
	if stuckErr != "" {
		res.Hints = append(res.Hints, stuckErr)
	}
	return res, raw, nil
}

// stuckStateLabel 把可选的状态过滤条件变成摘要里的可读文本。
func stuckStateLabel(state string) string {
	if state == "" {
		return "任意状态"
	}
	return state
}

// looksLikePGID 判断字符串是否形如 2.4a：两段十六进制数字。
func looksLikePGID(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
	}
	return true
}
