package feishu

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/microyahoo/infra-agent/internal/access"
	"github.com/microyahoo/infra-agent/internal/agent/llm"
	"github.com/microyahoo/infra-agent/pkg/doctor"
)

// ClusterConfig 描述机器人可以访问的一套存储集群。
type ClusterConfig struct {
	Name           string
	Host           string
	Port           int
	User           string
	PrivateKeyPath string
	Password       string
	KnownHostsPath string
}

// SessionManager 按群（chat_id）维护诊断会话。
//
// 群里的追问要能看到上一轮查过什么，因此会话必须留存；
// 但 SSH 连接和模型客户端不能无限累积，所以带空闲回收。
type SessionManager struct {
	cfg     Config
	mu      sync.Mutex
	entries map[string]*sessionEntry
}

type sessionEntry struct {
	agent     *doctor.Agent
	cluster   *access.Cluster
	clusterID string
	lastUsed  time.Time
	mu        sync.Mutex // 同一个群串行处理，避免两条消息交叉
}

func newSessionManager(cfg Config) *SessionManager {
	return &SessionManager{cfg: cfg, entries: make(map[string]*sessionEntry)}
}

// get 返回该群的会话，必要时新建。
func (m *SessionManager) get(ctx context.Context, chatID, clusterName string) (*sessionEntry, error) {
	m.mu.Lock()
	if e, ok := m.entries[chatID]; ok && e.clusterID == clusterName {
		e.lastUsed = time.Now()
		m.mu.Unlock()
		return e, nil
	}
	m.mu.Unlock()

	cc, ok := m.cfg.clusterByName(clusterName)
	if !ok {
		return nil, fmt.Errorf("没有名为 %q 的集群配置", clusterName)
	}

	cluster, err := access.Dial(ctx, access.Config{
		Host:           cc.Host,
		Port:           cc.Port,
		User:           cc.User,
		PrivateKeyPath: cc.PrivateKeyPath,
		Password:       cc.Password,
		KnownHostsPath: cc.KnownHostsPath,
	})
	if err != nil {
		return nil, fmt.Errorf("接入集群 %s 失败: %w", cc.Name, err)
	}

	agent, _, err := doctor.Open(ctx, doctor.OpenConfig{
		Cluster:          cluster,
		Model:            m.cfg.newModel(),
		SourceDir:        m.cfg.SourceDir,
		MaxRounds:        m.cfg.MaxRounds,
		MaxCallsPerRound: m.cfg.MaxCallsPerRound,
		MaxCallsPerTurn:  m.cfg.MaxCallsPerTurn,
	})
	if err != nil {
		cluster.Close()
		return nil, err
	}

	e := &sessionEntry{agent: agent, cluster: cluster, clusterID: clusterName, lastUsed: time.Now()}
	m.mu.Lock()
	// 若同名会话在并发时已被建好，保留先建的那个
	if old, ok := m.entries[chatID]; ok && old.clusterID == clusterName {
		m.mu.Unlock()
		cluster.Close()
		return old, nil
	}
	if old, ok := m.entries[chatID]; ok {
		old.cluster.Close()
	}
	m.entries[chatID] = e
	m.mu.Unlock()
	return e, nil
}

// cleanupIdle 关闭长时间未使用的会话。
func (m *SessionManager) cleanupIdle(idle time.Duration) {
	if idle <= 0 {
		return
	}
	cutoff := time.Now().Add(-idle)
	m.mu.Lock()
	var dead []*sessionEntry
	for id, e := range m.entries {
		if e.lastUsed.Before(cutoff) {
			dead = append(dead, e)
			delete(m.entries, id)
		}
	}
	m.mu.Unlock()
	for _, e := range dead {
		e.cluster.Close()
	}
}

// Close 关闭全部会话。
func (m *SessionManager) Close() {
	m.mu.Lock()
	all := make([]*sessionEntry, 0, len(m.entries))
	for _, e := range m.entries {
		all = append(all, e)
	}
	m.entries = map[string]*sessionEntry{}
	m.mu.Unlock()
	for _, e := range all {
		e.cluster.Close()
	}
}

// modelFactory 便于测试替换。
func (c Config) newModel() llm.ChatModel {
	if c.NewModel != nil {
		return c.NewModel()
	}
	return llm.NewClient(llm.Config{
		BaseURL:    c.LLM.BaseURL,
		APIKey:     c.LLM.APIKey,
		Model:      c.LLM.Model,
		MaxTokens:  c.LLM.MaxTokens,
		MaxRetries: c.LLM.MaxRetries,
	})
}

// clusterByName 返回指定集群，空名时用默认集群。
func (c Config) clusterByName(name string) (ClusterConfig, bool) {
	if name == "" {
		return c.Cluster, c.Cluster.Host != ""
	}
	if cc, ok := c.Clusters[name]; ok {
		return cc, true
	}
	return ClusterConfig{}, false
}
