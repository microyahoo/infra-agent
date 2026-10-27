package doctor

import (
	"github.com/microyahoo/infra-agent/pkg/doctor/tools"
)

// toolsRegistryForTest 用 nil runner 构建某个 profile 的工具注册表。
func toolsRegistryForTest(p Profile) *tools.Registry {
	return tools.RegistryFrom(p.Tools(nil, ""))
}
