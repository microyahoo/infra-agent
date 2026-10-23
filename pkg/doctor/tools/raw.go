package tools

import (
	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

// newRaw 把一次执行结果整理成证据材料。
//
// 真实 Cluster.Run 会回填 Output.Command；测试替身等实现可能留空，
// 此时按 argv 重建，保证证据里始终有可复现的命令文本。
func newRaw(node string, argv []string, out access.Output) *Raw {
	cmd := out.Command
	if cmd == "" {
		cmd = access.BuildCommand(argv)
	}
	return &Raw{
		Node:      node,
		Command:   cmd,
		Output:    out.Stdout,
		Stderr:    out.Stderr,
		Duration:  out.Duration,
		ExitCode:  out.ExitCode,
		Truncated: out.Truncated,
	}
}
