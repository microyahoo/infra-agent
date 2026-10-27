package access

// Policy 只读策略。在命令真正执行前校验 argv。
// 这是「硬只读」的执行点：工具层即使写错，也无法在此通过。
//
// 采用「禁止变更」而不是「白名单允许」：白名单把诊断能力锁在作者预设的
// 场景里，遇到没想到的故障就无手段可查；禁止清单保留"不能变更"这个
// 硬约束，同时让诊断可以自由探索。实现见 policy_deny.go。
type Policy struct {
	deny *denyPolicy
}

// DefaultPolicy 返回默认只读策略。
func DefaultPolicy() *Policy {
	return DefaultDenyPolicy()
}

// Check 校验一条 argv 是否允许执行。
func (p *Policy) Check(argv []string) error {
	if p.deny == nil {
		return nil
	}
	return p.checkDeny(argv)
}
