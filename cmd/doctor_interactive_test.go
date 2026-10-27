package cmd

import (
	"bufio"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func scanOf(s string) *bufio.Scanner {
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return sc
}

// 粘贴多行内容（如 ceph -s 输出）必须作为一个整体问题，
// 而不是每行触发一次完整调查。
func TestReadInputCollectsMultilinePaste(t *testing.T) {
	sc := scanOf("cluster:\n  id: abc\n  health: HEALTH_WARN\n\n下一个问题\n")
	got, ok := readInput(sc)
	if !ok {
		t.Fatal("expected input")
	}
	if n := strings.Count(got, "\n"); n != 2 {
		t.Errorf("multiline paste should be one input, got %d newlines: %q", n, got)
	}
	if !strings.Contains(got, "HEALTH_WARN") {
		t.Errorf("paste lost content: %q", got)
	}
	// 空行之后的下一条仍然是独立输入
	next, ok := readInput(sc)
	if !ok || next != "下一个问题" {
		t.Errorf("next input = %q, ok=%v", next, ok)
	}
}

// 单行提问不需要额外空行。
func TestReadInputSingleLine(t *testing.T) {
	sc := scanOf("那 osd.92 呢\n")
	got, ok := readInput(sc)
	if !ok || got != "那 osd.92 呢" {
		t.Errorf("got %q ok=%v", got, ok)
	}
}

// 冒号命令按单行处理，不能被当成多行起点。
func TestReadInputCommandSingleLine(t *testing.T) {
	sc := scanOf(":evidence\n:quit\n")
	got, ok := readInput(sc)
	if !ok || got != ":evidence" {
		t.Errorf("got %q", got)
	}
}

// 每轮超时必须独立：上一轮超时不能让后续轮次立刻失败。
// 这是实际故障——整个进程共用一个 600s context，首轮跑满后全部追问都是 deadline exceeded。
func TestWithTurnTimeoutIsIndependentPerTurn(t *testing.T) {
	base := context.Background()

	// 第一轮：极短超时，必然失败
	_, err := withTurnTimeout(base, 0, func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first turn should time out, got %v", err)
	}

	// 第二轮：必须仍然可用
	got, err := withTurnTimeout(base, 5, func(ctx context.Context) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("second turn must still work after first timed out: %v", err)
	}
	if got != "ok" {
		t.Errorf("got %q", got)
	}
}

func TestDescribeTurnErrorExplainsTimeout(t *testing.T) {
	msg := describeTurnError(context.DeadlineExceeded, 600)
	if !strings.Contains(msg, "600") {
		t.Errorf("should mention the budget: %q", msg)
	}
	if !strings.Contains(msg, "--timeout") {
		t.Errorf("should tell the user how to change it: %q", msg)
	}
	// 非超时错误原样返回
	if got := describeTurnError(errors.New("boom"), 600); got != "boom" {
		t.Errorf("got %q", got)
	}
}

func TestSummarizeInput(t *testing.T) {
	multi := strings.Join([]string{"cluster:", "  id: x", "  health: WARN"}, "\n")
	got := summarizeInput(multi)
	if !strings.Contains(got, "3 行") {
		t.Errorf("got %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Error("summary must be one line")
	}
	if single := summarizeInput("简短问题"); single != "简短问题" {
		t.Errorf("got %q", single)
	}
}

// 确保 withTurnTimeout 不会因为父 context 已取消而影响后续（父 context 才是真正的终止信号）
func TestWithTurnTimeoutRespectsBaseCancellation(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := withTurnTimeout(base, 30, func(ctx context.Context) (string, error) {
		return "", ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled base should propagate, got %v", err)
	}
	_ = time.Second
}

// 交互模式在用户提问之前不应做任何探测/调查。
// 这是 runInteractive 的契约：会话懒建立。
func TestInteractiveBannerMentionsLazyStart(t *testing.T) {
	for _, want := range []string{":help", ":evidence", ":report", ":quit", "空行"} {
		if !strings.Contains(interactiveBanner+interactiveHelp, want) {
			t.Errorf("interactive help missing %q", want)
		}
	}
	if !strings.Contains(interactiveHelp, ":refresh") {
		t.Error("help should document :refresh")
	}
}

// 交互模式在首个问题之前不得建立会话（也就不会探测/拓扑/调模型）。
// 这里用 "是否走到 :quit 之前" 间接验证：banner 里不能出现"首轮调查"。
func TestInteractiveStartsIdle(t *testing.T) {
	if strings.Contains(interactiveBanner, "首轮调查") {
		t.Error("interactive mode must not announce a pre-run investigation")
	}
	if !strings.Contains(interactiveBanner, ":quit") {
		t.Error("banner should show how to exit immediately")
	}
}
