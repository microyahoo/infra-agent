package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

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
func runInteractiveSession(ctx context.Context, d *doctor.Doctor) error {
	sess, err := d.OpenSession(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "🔎 首轮调查 ...")
	first, err := sess.Ask(ctx, doctorOpts.Question)
	if err != nil {
		return err
	}
	return runInteractive(ctx, sess, first)
}

// runInteractive 在一次诊断之后进入问答循环。
//
// 复用同一次会话的对话历史与证据链：模型能引用之前查到的内容，
// 而不是每问一次就从头再查一遍。
func runInteractive(ctx context.Context, sess *doctor.Session, first *loop.Result) error {
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
		if !in.Scan() {
			return nil // EOF / Ctrl-D
		}
		line := strings.TrimSpace(in.Text())
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

		fmt.Fprintf(os.Stderr, "🔎 正在调查：%s\n", line)
		next, err := sess.FollowUp(ctx, cur, line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ 调查失败：%v\n", err)
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
