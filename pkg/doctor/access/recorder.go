package access

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// sessionFile 是录制文件名，与回放共用。
const sessionFile = "session.jsonl"

// ReplayEntry 是一次远程执行的录制快照。
// 字段保持「原样输出」：回放时不做截断、不做再分类，
// 保证同一份录制在任何环境下都得到完全一致的工具行为。
type ReplayEntry struct {
	Node      string        `json:"node"`
	Argv      []string      `json:"argv"`
	Stdout    string        `json:"stdout"`
	Stderr    string        `json:"stderr"`
	ExitCode  int           `json:"exit_code"`
	Duration  time.Duration `json:"duration"`
	Error     string        `json:"error,omitempty"`
	ErrorKind string        `json:"error_kind,omitempty"`
}

// Recorder 把每次远程执行追加写入 <dir>/session.jsonl。
//
// 一次录制只对应一个集群：目录名建议用集群 ID，
// 这样 40 个集群就是 40 个 testdata 目录，每份录制都是永久的测试夹具。
// Recorder 可被多个 goroutine 并发调用。
type Recorder struct {
	mu   sync.Mutex
	f    *os.File
	bw   *bufio.Writer
	enc  *json.Encoder
	path string
	err  error // 首次写入失败后记录，Close 时一并返回
}

// NewRecorder 创建录制器，必要时创建目录。
// 已存在的 session.jsonl 会被截断：每次录制都是一段新的会话。
func NewRecorder(dir string) (*Recorder, error) {
	if dir == "" {
		return nil, fmt.Errorf("录制目录为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建录制目录 %q 失败: %w", dir, err)
	}
	path := filepath.Join(dir, sessionFile)
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("创建录制文件 %q 失败: %w", path, err)
	}
	bw := bufio.NewWriter(f)
	return &Recorder{f: f, bw: bw, enc: json.NewEncoder(bw), path: path}, nil
}

// Path 返回录制文件路径。
func (r *Recorder) Path() string { return r.path }

// Record 记录一次成功（或仅有非零退出码）的执行。
func (r *Recorder) Record(node string, argv []string, out Output) {
	r.write(node, argv, out, "")
}

// RecordError 记录一次失败的执行，并保留 *ToolError 的语义分类，
// 使回放时能重现 timeout / unreachable 等原始错误类型。
// Output 本身没有 Err 字段（它被工具层广泛使用），因此错误单独传参。
func (r *Recorder) RecordError(node string, argv []string, out Output, err error) {
	msg, kind := "", ""
	if err != nil {
		msg = err.Error()
		var te *ToolError
		if errors.As(err, &te) {
			kind = string(te.Kind)
		}
	}
	r.write(node, argv, out, msg, kind)
}

func (r *Recorder) write(node string, argv []string, out Output, msg string, kind ...string) {
	entry := ReplayEntry{
		Node:     node,
		Argv:     append([]string(nil), argv...),
		Stdout:   out.Stdout,
		Stderr:   out.Stderr,
		ExitCode: out.ExitCode,
		Duration: out.Duration,
		Error:    msg,
	}
	if len(kind) > 0 {
		entry.ErrorKind = kind[0]
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return // 已出过错，不再重复尝试
	}
	if r.f == nil {
		r.err = fmt.Errorf("录制器已关闭")
		return
	}
	if err := r.enc.Encode(&entry); err != nil {
		r.err = fmt.Errorf("写入录制失败: %w", err)
	}
}

// Close 刷盘并关闭文件，返回期间发生的首个写入错误。
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return r.err
	}
	flushErr := r.bw.Flush()
	closeErr := r.f.Close()
	r.f = nil
	if r.err == nil {
		if flushErr != nil {
			r.err = fmt.Errorf("刷新录制失败: %w", flushErr)
		} else if closeErr != nil {
			r.err = fmt.Errorf("关闭录制文件失败: %w", closeErr)
		}
	}
	return r.err
}
