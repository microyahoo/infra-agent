// Package evidence 记录每次工具调用的证据链。
//
// 证据链（Chain）是防止模型凭空捏造结论的核心机制：模型只能引用证据 ID，
// 最终报告根据 ID 回填真实的命令与输出。
package evidence

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Evidence 表示一次工具调用的完整记录。
//
// 模型在结论中只能引用 ID；Command、RawOutput 等字段由证据链回填，
// 从而保证报告中的事实均可溯源。
type Evidence struct {
	ID        string         `json:"id"`
	Round     int            `json:"round"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args,omitempty"`
	Node      string         `json:"node,omitempty"`
	Command   string         `json:"command,omitempty"`
	StartedAt time.Time      `json:"started_at"`
	Duration  time.Duration  `json:"duration"`
	ExitCode  int            `json:"exit_code"`
	RawOutput string         `json:"raw_output,omitempty"`
	Summary   string         `json:"summary,omitempty"`
	ErrorKind string         `json:"error_kind,omitempty"`
	ErrorMsg  string         `json:"error_message,omitempty"`
}

// Chain 是按插入顺序保存证据的线程安全证据链。
type Chain struct {
	mu      sync.RWMutex
	items   []Evidence
	nextID  int
	byID    map[string]int      // ID -> items 下标，便于 O(1) 查找
	calls   map[string]struct{} // 已执行过的调用 key，用于去重
	round   int                 // 当前轮次
	lastAdd int                 // 最近一次新增证据所在的轮次
}

// NewChain 创建空证据链。
func NewChain() *Chain {
	return &Chain{
		nextID: 1,
		byID:   make(map[string]int),
		calls:  make(map[string]struct{}),
	}
}

// Add 追加一条证据，自动分配 ev-NNN 形式的 ID，并在 StartedAt 为零值时补上当前时间。
// 返回的是实际存入链中的副本。
func (c *Chain) Add(e Evidence) Evidence {
	c.mu.Lock()
	defer c.mu.Unlock()

	if e.StartedAt.IsZero() {
		e.StartedAt = time.Now()
	}
	// 已带 ID 的证据（例如会话开始时预置的快照证据）保留其 ID，
	// 保证提示词里给出的编号与链里的一致——否则模型引用的 ID 对不上。
	if e.ID != "" {
		if _, exists := c.byID[e.ID]; !exists {
			c.byID[e.ID] = len(c.items)
			c.items = append(c.items, e)
			// 把 nextID 推到该编号之后，避免后续新增与它撞号。
			var n int
			if _, err := fmt.Sscanf(e.ID, "ev-%d", &n); err == nil && n >= c.nextID {
				c.nextID = n + 1
			}
			return e
		}
	}
	e.ID = fmt.Sprintf("ev-%03d", c.nextID)
	c.nextID++

	c.byID[e.ID] = len(c.items)
	c.items = append(c.items, e)

	// 有新增证据说明本轮有进展，重置无进展计数。

	// 若调用方显式传入了工具名，则同时视为已执行过的调用，避免重复执行。
	if e.Tool != "" {
		c.calls[callKey(e.Tool, e.Args)] = struct{}{}
	}

	return e
}

// All 返回按插入顺序排列的证据副本。
func (c *Chain) All() []Evidence {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]Evidence, len(c.items))
	copy(out, c.items)
	return out
}

// Len 返回证据数量。
func (c *Chain) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.items)
}

// SeenCall 判断该工具调用（工具名 + 规范化参数）是否已经执行过。
func (c *Chain) SeenCall(tool string, args map[string]any) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	_, ok := c.calls[callKey(tool, args)]
	return ok
}

// MarkCall 显式标记该工具调用已执行过（用于 Add 之外的去重场景）。
func (c *Chain) MarkCall(tool string, args map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.calls[callKey(tool, args)] = struct{}{}
}

// callKey 由工具名与参数的规范化 JSON 生成调用去重键。
// json.Marshal 会对 map 的键排序，因此同一参数总能得到相同的键。
func callKey(tool string, args map[string]any) string {
	b, err := json.Marshal(args)
	if err != nil {
		// 参数不可序列化时退化为格式化文本，至少保证调用键稳定可比。
		return tool + "|" + fmt.Sprintf("%v", args)
	}
	return tool + "|" + string(b)
}
