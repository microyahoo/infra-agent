package doctor

import (
	"context"

	"github.com/microyahoo/infra-agent/internal/access"
	"github.com/microyahoo/infra-agent/internal/agent/llm"
	"github.com/microyahoo/infra-agent/internal/ceph/snapshot"
	"github.com/microyahoo/infra-agent/internal/ceph/tools"
)

// OpenConfig 建立一个诊断会话所需的全部参数。
type OpenConfig struct {
	Cluster          *access.Cluster
	Model            llm.ChatModel
	SourceDir        string
	MaxRounds        int
	MaxCallsPerRound int
	MaxCallsPerTurn  int
	OnEvent          func(string)
}

// Open 采集初始信息并构造一个可对话的调查助手。
//
// CLI 与飞书机器人都走这里，避免两处各写一遍"快照 + 工具注册 + 提示词"。
func Open(ctx context.Context, cfg OpenConfig) (*Agent, snapshot.Snapshot, error) {
	runner := ClusterRunner{Cluster: cfg.Cluster}

	snap, err := snapshot.Collect(ctx, runner)
	if err != nil {
		return nil, snap, err
	}

	reg := tools.RegistryFrom(tools.All(runner, cfg.SourceDir))
	agent := NewAgent(AgentConfig{
		Model:            cfg.Model,
		Registry:         reg,
		SystemPrompt:     AgentPrompt(snap),
		SeedEvidence:     snap.Evidence,
		OnEvent:          cfg.OnEvent,
		MaxRounds:        cfg.MaxRounds,
		MaxCallsPerRound: cfg.MaxCallsPerRound,
		MaxCallsPerTurn:  cfg.MaxCallsPerTurn,
	})
	return agent, snap, nil
}

// ClusterRunner 把 access.Cluster 适配成工具层需要的 Runner。
type ClusterRunner struct{ Cluster *access.Cluster }

func (r ClusterRunner) Run(ctx context.Context, node string, argv []string) (access.Output, error) {
	return r.Cluster.Run(ctx, node, argv)
}
func (r ClusterRunner) Capabilities(ctx context.Context, node string) access.Capabilities {
	return r.Cluster.Capabilities(ctx, node)
}
func (r ClusterRunner) Topology(ctx context.Context) (*access.Topology, error) {
	return r.Cluster.Topology(ctx)
}
func (r ClusterRunner) Nodes() []string { return r.Cluster.Nodes() }
