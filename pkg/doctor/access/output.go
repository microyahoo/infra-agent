package access

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Output 是一次远程命令执行的结果。
type Output struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Duration  time.Duration
	Node      string
	Command   string // 实际执行的命令串（已脱敏，用于审计与证据）
	Truncated bool
}

// ErrorKind 区分远程执行失败的语义，模型据此决定下一步。
type ErrorKind string

const (
	KindTimeout     ErrorKind = "timeout"     // 命令未在时限内返回，通常意味着组件无响应
	KindUnreachable ErrorKind = "unreachable" // 节点/服务不可达
	KindUnavailable ErrorKind = "unavailable" // 环境不具备该能力
	KindFailed      ErrorKind = "failed"      // 命令执行但返回非零
)

// ToolError 是带语义分类的执行错误。
type ToolError struct {
	Kind    ErrorKind
	Message string
}

func (e *ToolError) Error() string { return string(e.Kind) + ": " + e.Message }

var errDeadline = context.DeadlineExceeded

// classifyError 把底层错误转成带语义的 ToolError。
// 超时必须被单独识别：模型看到原始的 "context deadline exceeded" 只会反复重试，
// 或者误以为是自己参数写错，而它的真正含义往往是「目标组件无响应」。
func classifyError(err error, stderr string, exitCode int) *ToolError {
	if err == nil {
		if exitCode != 0 {
			return &ToolError{Kind: KindFailed, Message: firstNonEmpty(stderr, fmt.Sprintf("exit code %d", exitCode))}
		}
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ToolError{
			Kind:    KindTimeout,
			Message: "命令在时限内未返回，通常意味着对应组件无响应",
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "connection timed out") ||
		strings.Contains(msg, "ssh: handshake failed") {
		return &ToolError{Kind: KindUnreachable, Message: msg}
	}
	if strings.TrimSpace(stderr) != "" {
		return &ToolError{Kind: KindFailed, Message: stderr}
	}
	return &ToolError{Kind: KindFailed, Message: msg}
}

// unavailable 构造一个能力缺失错误。
func unavailable(format string, args ...any) *ToolError {
	return &ToolError{Kind: KindUnavailable, Message: fmt.Sprintf(format, args...)}
}

// truncateOutput 保留头尾、中间省略，避免超大输出冲爆上下文。
func truncateOutput(s string, limit int) (string, bool) {
	if limit <= 0 || len(s) <= limit {
		return s, false
	}
	half := limit / 2
	head := s[:half]
	tail := s[len(s)-half:]
	omitted := len(s) - len(head) - len(tail)
	return head + fmt.Sprintf("\n... [truncated %d bytes] ...\n", omitted) + tail, true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
