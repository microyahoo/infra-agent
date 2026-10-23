package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// 以下构造函数在后续任务中逐个替换为真实实现。
func NewPoolInfo(r Runner) Tool               { return &pendingTool{name: "pool_info"} }
func NewPGDetail(r Runner) Tool               { return &pendingTool{name: "pg_detail"} }
func NewOSDDetail(r Runner) Tool              { return &pendingTool{name: "osd_detail"} }
func NewDaemonLog(r Runner) Tool              { return &pendingTool{name: "daemon_log"} }
func NewSlowOps(r Runner) Tool                { return &pendingTool{name: "slow_ops"} }
func NewPerfCounters(r Runner) Tool           { return &pendingTool{name: "perf_counters"} }
func NewHostIO(r Runner) Tool                 { return &pendingTool{name: "host_io"} }
func NewReadLog(r Runner) Tool                { return &pendingTool{name: "read_log"} }
func NewHostCheck(r Runner) Tool              { return &pendingTool{name: "host_check"} }
func NewSourceLookup(r Runner, _ string) Tool { return &pendingTool{name: "ceph_source_lookup"} }

// pendingTool 是占位工具：报告自身尚未实现，避免被误当作可用能力。
type pendingTool struct{ name string }

func (p *pendingTool) Name() string        { return p.name }
func (p *pendingTool) Description() string { return "尚未实现" }
func (p *pendingTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (p *pendingTool) Execute(context.Context, json.RawMessage) (*Result, *Raw, error) {
	return nil, nil, fmt.Errorf("%s 尚未实现", p.name)
}
