package access

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// replayBufSize 是读取单行录制的缓冲区上限。
// ceph 的 json 输出动辄数 MB，默认 64KB 不够用，这里放到 8MB。
const replayBufSize = 8 << 20

// Replay 用一段录制替代真实集群：同一 node+argv 返回录制时的输出与错误。
// 它是 Cluster.Run 对外行为的离线替身，让开发与 CI 无需真实集群。
type Replay struct {
	entries map[string]ReplayEntry
	nodes   []string
}

// NewReplay 加载 <dir>/session.jsonl。
// 同一 key 出现多次时，以后出现的为准（与「时间上最后一次」语义一致）。
func NewReplay(dir string) (*Replay, error) {
	path := filepath.Join(dir, sessionFile)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开录制文件 %q 失败: %w", path, err)
	}
	defer f.Close()

	r := &Replay{entries: map[string]ReplayEntry{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), replayBufSize)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var e ReplayEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("解析录制 %q 第 %d 行失败: %w", path, lineNo, err)
		}
		r.entries[replayKey(e.Node, e.Argv)] = e
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取录制 %q 失败: %w", path, err)
	}

	seen := map[string]bool{}
	for _, e := range r.entries {
		if !seen[e.Node] {
			seen[e.Node] = true
			r.nodes = append(r.nodes, e.Node)
		}
	}
	sort.Strings(r.nodes)
	return r, nil
}

// Run 按 node+argv 回放一次执行。
// 未录制的调用直接报错而不是返回空输出：静默返回假数据会让诊断结论失真。
func (r *Replay) Run(ctx context.Context, node string, argv []string) (Output, error) {
	if err := ctx.Err(); err != nil {
		return Output{}, &ToolError{Kind: KindTimeout, Message: err.Error()}
	}
	e, ok := r.entries[replayKey(node, argv)]
	if !ok {
		return Output{}, unavailable("节点 %q 上的命令未录制: %s", node, strings.Join(argv, " "))
	}
	out := Output{
		Stdout:   e.Stdout,
		Stderr:   e.Stderr,
		ExitCode: e.ExitCode,
		Duration: e.Duration,
		Node:     e.Node,
		Command:  BuildCommand(e.Argv),
	}
	if e.Error != "" {
		kind := ErrorKind(e.ErrorKind)
		if kind == "" {
			kind = KindFailed
		}
		return out, &ToolError{Kind: kind, Message: e.Error}
	}
	return out, nil
}

// Nodes 返回录制中出现过的节点，已排序，便于稳定展示。
func (r *Replay) Nodes() []string {
	out := make([]string, len(r.nodes))
	copy(out, r.nodes)
	return out
}

// Has 报告某个 node+argv 是否已录制。
func (r *Replay) Has(node string, argv []string) bool {
	_, ok := r.entries[replayKey(node, argv)]
	return ok
}

// replayKey 是录制的查找键：节点 + argv 原样拼接。
func replayKey(node string, argv []string) string {
	return node + "|" + strings.Join(argv, " ")
}
