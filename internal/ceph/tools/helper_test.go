package tools

// errString 是测试用错误，避免为单条失败引入额外依赖。
type errString string

func (e errString) Error() string { return string(e) }
