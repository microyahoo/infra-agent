package doctor

import (
	"context"
	"strings"
	"time"

	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
	"github.com/microyahoo/infra-agent/pkg/doctor/report"
	"github.com/microyahoo/infra-agent/pkg/doctor/tools"
)

// Session 是一次登录后的完整诊断会话。
//
// 与 Run 的区别：Session 保留快照、工具注册表和对话历史，
// 因此可以在同一次连接上连续追问，而不是每条问题都重新探测、重新查一遍。
type Session struct {
	d         *Doctor
	runner    *clusterRunner
	reg       *tools.Registry
	snap      snapshotResult
	startedAt time.Time
}

// OpenSession 建立会话并完成一次只读快照。
func (d *Doctor) OpenSession(ctx context.Context) (*Session, error) {
	if d.cfg.Cluster == nil {
		return nil, errNoCluster
	}
	if d.cfg.Model == nil {
		return nil, errNoModel
	}

	started := time.Now()
	runner := &clusterRunner{Cluster: d.cfg.Cluster}
	return &Session{
		d:         d,
		runner:    runner,
		reg:       tools.RegistryFrom(tools.All(runner, d.cfg.SourceDir)),
		snap:      d.snapshot(ctx),
		startedAt: started,
	}, nil
}

// Ask 发起首轮调查。
func (s *Session) Ask(ctx context.Context, question string) (*loop.Result, error) {
	lr := s.newRunner(question)
	return lr.Run(ctx, loop.Task{ClusterID: s.snap.ClusterID, Question: questionOf(question)})
}

// FollowUp 在已有会话上追问。
func (s *Session) FollowUp(ctx context.Context, prev *loop.Result, question string) (*loop.Result, error) {
	// 集群状态会变。沿用会话开始时的快照会让模型拿过期的 HEALTH_OK
	// 去解释当前的 HEALTH_WARN——实际观察到模型在报告里写"与既有证据冲突"。
	// 因此每轮追问前重取一次快照。
	s.Refresh(ctx)
	lr := s.newRunner(question)
	return lr.FollowUp(ctx, prev, question)
}

// Refresh 只重取健康状态。
//
// 拓扑与能力在会话期间基本不变，重取只会拖慢追问；
// 但健康状况随时会变，必须刷新，否则模型会拿过期的 HEALTH_OK 解释当前告警。
func (s *Session) Refresh(ctx context.Context) {
	s.d.refreshHealth(ctx, &s.snap)
}

// Report 把一轮调查结果渲染成报告。
func (s *Session) Report(res *loop.Result, runErr error) (*report.Report, error) {
	return s.d.buildReport(s.runner, s.snap, s.startedAt, res, runErr)
}

// newRunner 用会话的快照与工具表构造一轮调查。
func (s *Session) newRunner(question string) *loop.SessionRunner {
	return loop.NewRunner(loop.RunnerConfig{
		Model:            s.d.cfg.Model,
		Registry:         s.reg,
		SystemPrompt:     SystemPrompt(s.snap.toSnapshot(), questionOf(question)),
		MaxRounds:        s.d.cfg.MaxRounds,
		MaxToolCalls:     s.d.cfg.MaxToolCalls,
		MaxPerTool:       s.d.cfg.MaxPerTool,
		MaxCallsPerRound: s.d.cfg.MaxCallsPerRound,
		OnProgress:       s.d.cfg.OnProgress,
	})
}

// questionOf 为空时使用默认的全面体检问题。
func questionOf(q string) string {
	if strings.TrimSpace(q) == "" {
		return defaultQuestion
	}
	return q
}
