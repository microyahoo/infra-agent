package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/microyahoo/infra-agent/pkg/doctor"
)

const interactiveBanner = `
直接说你想查什么就行，例如：
  · 集群为啥 warn 了
  · osd.92 这块盘正常吗
  · 最近写入延迟高不高
  · ceph -s 的输出直接粘贴也行（空行结束）

输入 :help 看命令，:quit 退出。
`

const interactiveHelp = `
  :help            显示帮助
  :evidence        列出已执行的查询（证据）
  :quit / :exit    退出

其余输入都交给调查助手。多行内容以空行结束。
`

// runInteractive 进入对话循环。
//
// 助手按需建立：第一次提问时才连接、探测、取拓扑，之后复用同一会话，
// 模型能看到之前聊过什么、查过什么，因此"能不能更准确一点"这类反馈
// 会被当成对话的一部分，而不是又一次全新的体检。
func runInteractive(ctx context.Context, newAgent func(string) (*doctor.Agent, error), apiKey string, needKey bool, opt resolvedDoctorOptions) error {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	fmt.Fprint(os.Stderr, interactiveBanner)

	var agent *doctor.Agent
	for {
		fmt.Fprint(os.Stderr, "\n❯ ")
		text, ok := readInput(in)
		if !ok {
			return nil
		}
		if text == "" {
			continue
		}

		switch text {
		case ":quit", ":exit", ":q":
			return nil
		case ":help", ":h":
			fmt.Fprint(os.Stderr, interactiveHelp)
			continue
		case ":evidence":
			printEvidence(agent)
			continue
		}

		if agent == nil {
			if needKey && apiKey == "" {
				fmt.Fprintf(os.Stderr, "✗ 未找到 API Key：请设置环境变量 %s，或在配置文件 doctor.llm.api_key 中填写\n", opt.APIKeyEnv)
				continue
			}
			var err error
			agent, err = withTimeout(ctx, opt.Timeout, func(c context.Context) (*doctor.Agent, error) {
				return newAgent(apiKey)
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "✗ 初始化失败：%v\n", err)
				agent = nil
				continue
			}
		}

		reply, err := withTimeout(ctx, opt.Timeout, func(c context.Context) (string, error) {
			return agent.Send(c, text)
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %s\n", describeError(err, opt.Timeout))
			continue
		}
		if strings.TrimSpace(reply) == "" {
			fmt.Fprintln(os.Stderr, "（模型没有返回内容）")
			continue
		}
		fmt.Fprintln(os.Stdout, "")
		fmt.Fprintln(os.Stdout, reply)
	}
}

// readInput 读一条输入：单行直接回车，多行以空行结束。
//
// 逐行读会把粘贴的 ceph -s 输出拆成十几个独立问题，每个都触发一次调查。
func readInput(in *bufio.Scanner) (string, bool) {
	if !in.Scan() {
		return "", false
	}
	first := in.Text()
	trimmed := strings.TrimSpace(first)
	if trimmed == "" || strings.HasPrefix(trimmed, ":") {
		return trimmed, true
	}

	lines := []string{first}
	for in.Scan() {
		next := in.Text()
		if strings.TrimSpace(next) == "" {
			break
		}
		lines = append(lines, next)
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), true
}

// withTimeout 为单次操作新建超时预算。每次调用都从父上下文派生，
// 一次超时不会让后续操作全部失效。
func withTimeout[T any](base context.Context, seconds int, fn func(context.Context) (T, error)) (T, error) {
	c, cancel := context.WithTimeout(base, time.Duration(seconds)*time.Second)
	defer cancel()
	return fn(c)
}

func describeError(err error, seconds int) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("超过 %d 秒未完成。可以用 --timeout 调大，或把问题问得更具体一些", seconds)
	}
	return err.Error()
}

// printEvidence 打印已执行的查询清单。
func printEvidence(a *doctor.Agent) {
	if a == nil || a.Evidence() == nil || a.Evidence().Len() == 0 {
		fmt.Fprintln(os.Stderr, "还没有执行过查询")
		return
	}
	chain := a.Evidence()
	fmt.Fprintf(os.Stderr, "已执行 %d 次查询：\n", chain.Len())
	for _, e := range chain.All() {
		status := "✓"
		detail := e.Summary
		if e.ErrorKind != "" {
			status = "✗"
			detail = e.ErrorKind + ": " + e.ErrorMsg
		}
		fmt.Fprintf(os.Stderr, "  %s %s  %-16s %s\n", status, e.ID, e.Tool, truncateLine(detail, 80))
	}
}
