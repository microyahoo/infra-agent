// Package session 把连接、工具表、快照与内置 agent 装配成一次可用会话。
package session

import (
	"context"
	"time"

	"github.com/microyahoo/infra-agent/internal/access"
	"github.com/microyahoo/infra-agent/internal/agent"
	"github.com/microyahoo/infra-agent/internal/agent/llm"
	"github.com/microyahoo/infra-agent/internal/ceph/snapshot"
	"github.com/microyahoo/infra-agent/internal/ceph/tools"
	"github.com/microyahoo/infra-agent/internal/config"
)

// Options 是建立一次会话所需的参数。
type Options struct {
	Cluster         config.Cluster
	Policy          config.Policy
	SourceDir       string
	CollectSnapshot bool
	OnEvent         func(string)
}

// Session 是一套集群上的运行时。
type Session struct {
	cluster  *access.Cluster
	registry *tools.Registry
	snap     snapshot.Snapshot
	opts     Options
}

// Open 建立 SSH 连接，可选地采集快照，并装配工具表。
func Open(ctx context.Context, opts Options) (*Session, error) {
	cluster, err := access.Dial(ctx, access.Config{
		Host:           opts.Cluster.Host,
		Port:           opts.Cluster.Port,
		User:           opts.Cluster.User,
		Password:       opts.Cluster.Password,
		PrivateKeyPath: config.ExpandHome(opts.Cluster.PrivateKeyPath),
		KnownHostsPath: config.ExpandHome(opts.Cluster.KnownHostsPath),
		CommandTimeout: time.Duration(opts.Cluster.CommandTimeout) * time.Second,
		ProbeTimeout:   time.Duration(opts.Cluster.ProbeTimeout) * time.Second,
		ExtraBinaries:  opts.Policy.ExtraBinaries,
		OnProgress:     opts.OnEvent,
	})
	if err != nil {
		return nil, err
	}

	s := &Session{cluster: cluster, opts: opts}
	if opts.CollectSnapshot {
		s.snap, err = snapshot.Collect(ctx, cluster)
		if err != nil {
			cluster.Close()
			return nil, err
		}
	}
	// access.Cluster 已经实现了工具层需要的 Runner 接口：
	// Run / Capabilities / Topology / Nodes。不再需要额外的适配器类型。
	s.registry = tools.RegistryFrom(tools.All(cluster, opts.SourceDir))
	return s, nil
}

// Cluster 返回底层连接，供 dry-run 与 self-check 使用。
func (s *Session) Cluster() *access.Cluster { return s.cluster }

// Registry 返回工具表。
func (s *Session) Registry() *tools.Registry { return s.registry }

// Snapshot 返回会话起点采集到的快照。
func (s *Session) Snapshot() snapshot.Snapshot { return s.snap }

// NewAgent 用当前会话的工具表与快照构造内置 agent。
func (s *Session) NewAgent(model llm.ChatModel, loop config.Loop) *agent.Agent {
	return agent.NewAgent(agent.Config{
		Model:            model,
		Registry:         s.registry,
		SystemPrompt:     agent.SystemPrompt(s.snap),
		SeedEvidence:     s.snap.Evidence,
		OnEvent:          s.opts.OnEvent,
		MaxRounds:        loop.MaxRounds,
		MaxCallsPerRound: loop.MaxCallsPerRound,
		MaxCallsPerTurn:  loop.MaxToolCalls,
	})
}

// Close 释放 SSH 连接。
func (s *Session) Close() error {
	if s.cluster == nil {
		return nil
	}
	return s.cluster.Close()
}
