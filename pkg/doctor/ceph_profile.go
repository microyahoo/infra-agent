package doctor

import (
	"context"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/doctor/tools"
)

// CephProfile 是 Rook / cephadm 部署的 Ceph 集群。
//
// 它把已有的 Ceph 专用逻辑（快照采集、16 个工具、关联规则）
// 包在 Profile 接口后面；核心循环对此一无所知。
type CephProfile struct{}

func (CephProfile) Name() string { return "ceph" }

// Detect 判断是否为 Ceph：宿主机有 ceph CLI，或能在 Rook 命名空间里找到 CephCluster。
func (CephProfile) Detect(ctx context.Context, c Collector) bool {
	nodes := c.Nodes()
	if len(nodes) == 0 {
		return false
	}
	caps := c.Capabilities(ctx, nodes[0])
	return caps.CephCLI || caps.RookNamespace != ""
}

func (CephProfile) Snapshot(ctx context.Context, c Collector, srcDir string) (Snapshot, error) {
	return CollectSnapshotFrom(ctx, c)
}

func (CephProfile) Tools(r tools.Runner, srcDir string) []tools.Tool {
	return tools.All(r, srcDir)
}

func (CephProfile) Hints(s Snapshot) string {
	var b strings.Builder
	b.WriteString("## 关于这套系统\n\n")
	b.WriteString("这是 Rook 部署的 Ceph 集群。对比 Ceph 自身的状态与 Rook pod 的状态：" +
		"daemon 起不来通常先在 pod 事件里体现，之后才反映到 ceph health 上。\n")
	if s.Capabilities.Backend == "log-only" {
		b.WriteString("\n当前没有可用的 ceph CLI，只能读日志与主机信息。\n")
	}
	return b.String()
}

func init() {
	RegisterProfile(CephProfile{})
	RegisterProfile(GenericProfile{})
}
