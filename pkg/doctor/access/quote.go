package access

import (
	"fmt"
	"strings"
)

// shellQuote 把任意字符串包装成 POSIX sh 的单引号字面量。
// 单引号内的所有字符（含 $ ` ; | & > < 换行）都失去特殊含义，
// 唯一的例外是单引号本身，用 “'‘’‘'” 闭合-转义-重开的方式处理。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// BuildCommand 把 argv 转成一条可直接交给 SSH exec 通道的命令字符串。
// 保证每个参数都被完整转义，不会被远端 shell 拆分或解释。
func BuildCommand(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

// BuildCommandChecked 同 BuildCommand，但拒绝空 argv。
func BuildCommandChecked(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty argv")
	}
	return BuildCommand(argv), nil
}
