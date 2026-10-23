package access

import (
	"context"
	"strings"
)

// fakeResp 是一条预置命令的返回结果。
type fakeResp struct {
	stdout string
	stderr string
	exit   int
}

// fakeTransport 按 argv 拼成的命令串查表返回，未登记的命令一律按「命令不存在」处理。
// 这样测试只关心探测顺序，不依赖真实的 SSH。
type fakeTransport struct {
	responses map[string]fakeResp
}

func (f *fakeTransport) exec(_ context.Context, argv []string) (string, string, int) {
	key := strings.Join(argv, " ")
	if r, ok := f.responses[key]; ok {
		return r.stdout, r.stderr, r.exit
	}
	return "", "not found", 127
}
