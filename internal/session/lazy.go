package session

import (
	"context"
	"sync"

	"github.com/microyahoo/infra-agent/internal/ceph/tools"
)

// Lazy 在第一次被需要时才建立会话，并在失败后允许下次调用重试。
//
// MCP 客户端可能只列出工具、从不调用；集群也可能在启动瞬间不可达。
// 因此不在进程启动时连接。
type Lazy struct {
	Open func(context.Context) (*Session, error)

	mu  sync.Mutex
	cur *Session
}

// Registry 实现 mcpserver.Provider。
func (l *Lazy) Registry(ctx context.Context) (*tools.Registry, error) {
	s, err := l.get(ctx)
	if err != nil {
		return nil, err
	}
	return s.Registry(), nil
}

func (l *Lazy) get(ctx context.Context) (*Session, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur != nil {
		return l.cur, nil
	}
	s, err := l.Open(ctx)
	if err != nil {
		return nil, err
	}
	l.cur = s
	return s, nil
}

// Reset 丢弃当前会话；下一次调用会重新连接。
func (l *Lazy) Reset() {
	l.mu.Lock()
	s := l.cur
	l.cur = nil
	l.mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

// Close 关闭当前会话。
func (l *Lazy) Close() error {
	l.mu.Lock()
	s := l.cur
	l.cur = nil
	l.mu.Unlock()
	if s != nil {
		return s.Close()
	}
	return nil
}
