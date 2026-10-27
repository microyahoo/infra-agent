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
	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
	"github.com/microyahoo/infra-agent/pkg/doctor/report"
)

// interactiveBanner 是进入交互模式时的提示。
const interactiveBanner = `
进入交互模式。可以直接追问，例如：
  · 那 osd.92 呢？
  · 为什么 PG 一直 undersized？
  · 帮我看下 node03 的磁盘
输入 :help 查看命令，:quit 退出。
`

const interactiveHelp = `
可用命令：
  :help            显示本帮助
  :report [brief]  重新打印最近一次报告（默认完整版，brief 为精简版）
  :evidence        列出目前已收集的全部证据
  :quit / :exit    退出
其他任意输入都作为新的问题交给模型继续调查。
`

// runInteractiveSession 建立会话、跑首轮，然后进入问答循环。
//
// base 用于 Ctrl-C 传播，connectCtx 负责建连阶段；每一轮调查各自新建预算，
// 因为交互会话可能持续很久，共用一个 context 会在首轮跑满后让后续全部失败。
func runInteractiveSession(base, connectCtx context.Context, d *doctor.Doctor, opt resolvedDoctorOptions) error {
	sess, err := d.OpenSession(connectCtx)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "🔎 首轮调查 ...")
	first, err := withTurnTimeout(base, opt.Timeout, func(ctx context.Context) (*loop.Result, error) {
		return sess.Ask(ctx, opt.Question)
	})
	if err != nil {
		return err
	}
	return runInteractive(base, sess, first, opt)
}

// withTurnTimeout 为一轮调查新建独立超时。
//
// 每轮都从 base 派生，而不是复用上一轮的 ctx：
// 单轮超时只约束这一轮，超时后下一轮仍然可用。
func withTurnTimeout[T any](base context.Context, seconds int, fn func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(base, time.Duration(seconds)*time.Second)
	defer cancel()
	return fn(ctx)
}

// runInteractive 在一次诊断之后进入问答循环。
//
// 复用同一次会话的对话历史与证据链：模型能引用之前查到的内容，
// 而不是每问一次就从头再查一遍。
func runInteractive(ctx context.Context, sess *doctor.Session, first *loop.Result, opt resolvedDoctorOptions) error {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lastReport, _ := sess.Report(first, nil)
	if lastReport != nil {
		fmt.Fprintln(os.Stdout, renderOrEmpty(lastReport))
	}
	fmt.Fprint(os.Stderr, interactiveBanner)

	cur := first
	for {
		fmt.Fprint(os.Stderr, "\n❯ ")
		line, ok := readInput(in)
		if !ok {
			return nil // EOF / Ctrl-D
		}
		if line == "" {
			continue
		}

		switch line {
		case ":quit", ":exit", ":q":
			return nil
		case ":help", ":h":
			fmt.Fprint(os.Stderr, interactiveHelp)
			continue
		case ":evidence":
			printEvidence(cur)
			continue
		case ":report", ":r":
			if lastReport != nil {
				fmt.Fprintln(os.Stdout, renderOrEmpty(lastReport))
			} else {
				fmt.Fprintln(os.Stderr, "还没有可打印的报告")
			}
			continue
		case ":report brief", ":rb":
			if lastReport != nil {
				fmt.Fprintln(os.Stdout, report.RenderBrief(lastReport))
			}
			continue
		}

		fmt.Fprintf(os.Stderr, "🔎 正在调查：%s\n", summarizeInput(line))
		next, err := withTurnTimeout(ctx, opt.Timeout, func(turnCtx context.Context) (*loop.Result, error) {
			return sess.FollowUp(turnCtx, cur, line)
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ 调查失败：%v\n", describeTurnError(err, opt.Timeout))
			continue
		}
		cur = next

		rep, rerr := sess.Report(next, nil)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "✗ 报告生成失败：%v\n", rerr)
			continue
		}
		lastReport = rep
		fmt.Fprintln(os.Stdout, renderOrEmpty(rep))
	}
}

// readInput 读一条输入。
//
// 逐行读会把粘贴的多行内容（例如 ceph -s 的整段输出）拆成十几个独立问题，
// 每个都触发一整轮调查，既慢又乱。这里改成：先把已到达的行收拢，
// 遇到空行视为结束——单行提问仍然是一次回车，粘贴多行则作为一个整体。
func readInput(in *bufio.Scanner) (string, bool) {
	if !in.Scan() {
		return "", false
	}
	first := in.Text()
	trimmed := strings.TrimSpace(first)
	// 命令以 ':' 开头时按单行处理，免得 :report 之类还要再敲空行。
	if trimmed == "" || strings.HasPrefix(trimmed, ":") {
		return trimmed, true
	}

	var lines []string
	lines = append(lines, first)
	for in.Scan() {
		next := in.Text()
		if strings.TrimSpace(next) == "" {
			break
		}
		lines = append(lines, next)
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), true
}

// summarizeInput 把多行输入压成一行用于进度提示。
func summarizeInput(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= 1 {
		return s
	}
	return fmt.Sprintf("%s …（共 %d 行）", truncateLine(lines[0], 60), len(lines))
}

// describeTurnError 把超时错误翻译成可操作的提示。
func describeTurnError(err error, timeoutSec int) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("本轮超过 %d 秒未能结束。可以用 --timeout 调大单轮预算，或把问题拆小一些", timeoutSec)
	}
	return err.Error()
}

// printEvidence 打印已收集的证据清单。
func printEvidence(res *loop.Result) {
	if res == nil || res.Chain == nil || res.Chain.Len() == 0 {
		fmt.Fprintln(os.Stderr, "暂无证据")
		return
	}
	fmt.Fprintf(os.Stderr, "已收集 %d 条证据：\n", res.Chain.Len())
	for _, e := range res.Chain.All() {
		status := "✓"
		detail := e.Summary
		if e.ErrorKind != "" {
			status = "✗"
			detail = e.ErrorKind + ": " + e.ErrorMsg
		}
		fmt.Fprintf(os.Stderr, "  %s %s  %-18s %s\n", status, e.ID, e.Tool, truncateLine(detail, 80))
	}
}

// renderOrEmpty 渲染完整报告。
func renderOrEmpty(rep *report.Report) string {
	if rep == nil {
		return ""
	}
	return report.RenderText(rep)
}
