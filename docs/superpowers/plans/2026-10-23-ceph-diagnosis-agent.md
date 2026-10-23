# Ceph 集群诊断 Agent 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现 `infra-agent doctor --host <首节点IP>`，通过链式 SSH 进入 Rook Ceph 集群，由 LLM 工具调用循环自主调查，产出每条结论都挂着真实命令输出的诊断报告。

**Architecture:** 新建 `pkg/doctor`，分五层：`access`（链式 SSH + 能力探测 + 拓扑 + 只读沙箱）、`tools`（12 个诊断工具）、`llm`（OpenAI 工具调用客户端）、`loop`（预算受限的调查循环 + 证据链）、`report`（结构化结论 + 三种渲染）。全程只读，argv 严格转义，无 shell 语义泄漏。

**Tech Stack:** Go 1.21+、`golang.org/x/crypto/ssh`、`net/http`（OpenAI Chat Completions + tool_calls）、`spf13/cobra`、标准库 `testing`。

**参考设计：** `docs/superpowers/specs/2026-10-23-ceph-diagnosis-agent-design.md`

---

## 关键技术约束（实施前必读）

**SSH exec 协议只传一个字符串。** `session.Run(cmd string)` 把这个字符串交给远端 login shell。因此「argv 不经过 shell」只能通过**逐参数 POSIX 单引号转义**来保证。转义函数必须是：

```go
func shellQuote(s string) string {
    return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
```

任何绕过这个函数的拼命令都是安全漏洞。`access` 层的所有执行路径必须走 `BuildCommand(argv []string) string`。

**禁止使用 `ExecuteScript`。** 现有 `pkg/executor/ssh_executor.go` 有 `ExecuteScript`（通过 stdin 喂 bash），新代码不得使用 —— 它直接违反只读设计。

**日志过滤在 Go 侧完成。** 远端不跑 grep（除 `read_log` 工具的 `grep` 模式，它用固定形态的 `grep -F -c` 且 pattern 经校验）。

---

## 文件结构

```
pkg/doctor/
  access/
    quote.go            POSIX 转义 + 命令构造
    quote_test.go
    policy.go           只读白名单（argv 级校验）
    policy_test.go
    output.go           Output 类型、截断、超时分类
    output_test.go
    config.go           Config 类型与默认值
    dial.go             链式 SSH 建连
    dial_test.go
    probe.go            能力探测
    probe_test.go
    topology.go         拓扑发现与设备解析
    topology_test.go
    cluster.go          Cluster 类型，对外唯一入口
    cluster_test.go
  tools/
    registry.go         Tool 接口、Result、ToolError、注册表
    placeholders.go     工具构造占位（Task 9-14 逐条删除，Task 14 删文件）
    registry_test.go
    ceph_status.go      工具：集群全景
    ceph_status_test.go
    osd_tree.go         工具：OSD 树与容量
    osd_tree_test.go
    pool_info.go        工具：池容量与 PG 分布
    pool_info_test.go
    pg_detail.go        工具：PG 详情
    pg_detail_test.go
    osd_detail.go       工具：OSD 详情
    osd_detail_test.go
    daemon_log.go       工具：daemon 日志
    daemon_log_test.go
    slow_ops.go         工具：slow ops
    slow_ops_test.go
    perf_counters.go    工具：perf counters
    perf_counters_test.go
    host_io.go          工具：宿主机 IO/SMART/dmesg
    host_io_test.go
    read_log.go         工具：通用日志读取
    read_log_test.go
    host_check.go       工具：宿主机基础检查
    host_check_test.go
    source_lookup.go    工具：源码反查
    source_lookup_test.go
    fake_runner_test.go 共享的假 Runner
  llm/
    types.go            Message / ToolSpec / Reply / ToolCall
    client.go           OpenAI 兼容客户端
    client_test.go
  loop/
    evidence.go         证据链
    evidence_test.go
    loop.go             调查循环
    loop_test.go
    conclusion.go       结论 schema 与校验
    conclusion_test.go
  report/
    report.go           Report 类型
    render_text.go      完整文本报告
    render_text_test.go
    render_brief.go     值班视角
    render_brief_test.go
    render_json.go      JSON 渲染
    render_json_test.go
  doctor.go             编排入口
  doctor_test.go
cmd/
  doctor.go             新命令
testdata/
  replay/               回放数据（由 M1 录制）
```

---

## 阶段总览

| 阶段 | 任务 | 产出 |
|---|---|---|
| M1 | Task 1-7 | `access` 层完整可用，能对真集群列出拓扑并拒绝写操作 |
| M2 | Task 8-14 | 12 个工具 + 返回结构与错误分类 |
| M3 | Task 15-18 | LLM 客户端 + 调查循环 + 证据链 |
| M4 | Task 19-21 | 报告三种渲染 |
| M5 | Task 22-23 | 编排入口 + CLI 接入 |
| M6 | Task 24 | 录制基线 + 回归 |

---

# M1：access 层

### Task 1: POSIX 安全转义

**Files:**
- Create: `pkg/doctor/access/quote.go`
- Test: `pkg/doctor/access/quote_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/access/quote_test.go`：

```go
package access

import "testing"

func TestShellQuote(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"sdc", "'sdc'"},
		{"", "''"},
		{"a b", "'a b'"},
		{"it's", `'it'\''s'`},
		{"$(rm -rf /)", `'$(rm -rf /)'`},
		{"`id`", "'`id`'"},
		{"a;b|c&d>e<f", "'a;b|c&d>e<f'"},
		{"\n", "'\n'"},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildCommand(t *testing.T) {
	got := BuildCommand([]string{"ceph", "osd", "df", "-f", "json"})
	want := "ceph osd df -f json"
	if got != want {
		t.Errorf("BuildCommand = %q, want %q", got, want)
	}
}

func TestBuildCommandEscapesEveryArg(t *testing.T) {
	got := BuildCommand([]string{"grep", "-F", "a b"})
	want := "'grep' '-F' 'a b'"
	if got != want {
		t.Errorf("BuildCommand = %q, want %q", got, want)
	}
}

func TestBuildCommandRejectsEmptyArgv(t *testing.T) {
	if _, err := BuildCommandChecked(nil); err == nil {
		t.Fatal("expected error for empty argv")
	}
}

func TestBuildCommandNeutralizesInjection(t *testing.T) {
	// 参数里的 shell 元字符必须被单引号包裹，不产生额外命令
	got := BuildCommand([]string{"cat", "x; rm -rf /tmp/y"})
	want := "'cat' 'x; rm -rf /tmp/y'"
	if got != want {
		t.Fatalf("BuildCommand = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/access/ -run TestShellQuote -v`
Expected: FAIL — `undefined: shellQuote`

- [ ] **Step 3: 实现**

`pkg/doctor/access/quote.go`：

```go
package access

import (
	"fmt"
	"strings"
)

// shellQuote 把任意字符串包装成 POSIX sh 的单引号字面量。
// 单引号内的所有字符（含 $ ` ; | & > < 换行）都失去特殊含义，
// 唯一的例外是单引号本身，用 '\'' 闭合-转义-重开的方式处理。
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
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/doctor/access/ -run 'TestShellQuote|TestBuildCommand' -v`
Expected: PASS（5 个测试全部通过）

- [ ] **Step 5: 提交**

```bash
git add pkg/doctor/access/quote.go pkg/doctor/access/quote_test.go
git commit -m "feat(doctor/access): add POSIX shell quoting for argv execution"
```

---

### Task 2: 只读白名单策略

**Files:**
- Create: `pkg/doctor/access/policy.go`
- Test: `pkg/doctor/access/policy_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/access/policy_test.go`：

```go
package access

import (
	"strings"
	"testing"
)

func TestPolicyAllowsReadOnlyCeph(t *testing.T) {
	p := DefaultPolicy()
	allowed := [][]string{
		{"ceph", "-s"},
		{"ceph", "status", "-f", "json"},
		{"ceph", "health", "detail"},
		{"ceph", "df", "detail", "-f", "json"},
		{"ceph", "osd", "tree", "-f", "json"},
		{"ceph", "osd", "df", "-f", "json"},
		{"ceph", "osd", "metadata", "7"},
		{"ceph", "osd", "ok-to-stop", "7"},
		{"ceph", "pg", "stat"},
		{"ceph", "pg", "dump_stuck"},
		{"ceph", "pg", "2.4a", "query"},
		{"ceph", "tell", "osd.7", "dump_historic_ops"},
		{"ceph", "versions"},
		{"ceph", "log", "last", "200"},
		{"ceph", "mon", "stat"},
		{"ceph", "osd", "pool", "autoscale-status"},
	}
	for _, argv := range allowed {
		if err := p.Check(argv); err != nil {
			t.Errorf("expected allowed %v, got error: %v", argv, err)
		}
	}
}

func TestPolicyRejectsMutatingCeph(t *testing.T) {
	p := DefaultPolicy()
	rejected := [][]string{
		{"ceph", "osd", "out", "7"},
		{"ceph", "osd", "in", "7"},
		{"ceph", "osd", "rm", "7"},
		{"ceph", "osd", "crush", "remove", "osd.7"},
		{"ceph", "osd", "pool", "create", "x", "10"},
		{"ceph", "osd", "pool", "set", "x", "size", "3"},
		{"ceph", "osd", "pool", "delete", "x"},
		{"ceph", "osd", "set", "noout"},
		{"ceph", "config", "set", "global", "a", "b"},
		{"ceph", "tell", "osd.7", "injectargs", "--x"},
	}
	for _, argv := range rejected {
		if err := p.Check(argv); err == nil {
			t.Errorf("expected rejected %v, got nil", argv)
		}
	}
}

func TestPolicyRejectsUnknownBinary(t *testing.T) {
	p := DefaultPolicy()
	for _, argv := range [][]string{
		{"bash", "-c", "rm -rf /"},
		{"sh"},
		{"rm", "-f", "/tmp/x"},
		{"systemctl", "stop", "ceph-osd@7"},
		{"dd", "if=/dev/zero", "of=/dev/sda"},
	} {
		if err := p.Check(argv); err == nil {
			t.Errorf("expected rejected %v, got nil", argv)
		}
	}
}

func TestPolicyAllowsHostTools(t *testing.T) {
	p := DefaultPolicy()
	allowed := [][]string{
		{"iostat", "-x", "1", "3"},
		{"smartctl", "-a", "/dev/sdc"},
		{"smartctl", "-H", "/dev/sdc"},
		{"dmesg", "-T"},
		{"df", "-h"},
		{"free", "-m"},
		{"uptime"},
		{"lsblk", "-no", "PKNAME", "/dev/mapper/x"},
		{"tail", "-n", "500", "/var/log/ceph/fsid/ceph-osd.7.log"},
		{"grep", "-F", "-c", "error", "/var/log/ceph/fsid/ceph-osd.7.log"},
	}
	for _, argv := range allowed {
		if err := p.Check(argv); err != nil {
			t.Errorf("expected allowed %v, got error: %v", argv, err)
		}
	}
}

func TestPolicyRejectsTailWithUnsafePath(t *testing.T) {
	p := DefaultPolicy()
	for _, argv := range [][]string{
		{"tail", "-n", "10", "/etc/shadow"},
		{"tail", "-n", "10", "/root/.ssh/id_rsa"},
		{"tail", "-n", "../etc/passwd"},
		{"tail", "-n", "10", "/var/log/ceph/../../../etc/passwd"},
	} {
		if err := p.Check(argv); err == nil {
			t.Errorf("expected rejected %v, got nil", argv)
		}
	}
}

func TestPolicyRejectsGrepPatternStartingWithDash(t *testing.T) {
	p := DefaultPolicy()
	err := p.Check([]string{"grep", "-F", "--file=/etc/passwd", "x"})
	if err == nil {
		t.Fatal("expected rejected option-like grep pattern")
	}
	if !strings.Contains(err.Error(), "grep") && !strings.Contains(err.Error(), "rejected") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestPolicyRejectsKubectlWrite(t *testing.T) {
	p := DefaultPolicy()
	allowed := [][]string{
		{"kubectl", "-n", "rook-ceph", "get", "pod", "-l", "app=rook-ceph-tools"},
		{"kubectl", "-n", "rook-ceph", "logs", "rook-ceph-osd-5-abc", "-c", "osd"},
	}
	for _, argv := range allowed {
		if err := p.Check(argv); err != nil {
			t.Errorf("expected allowed %v, got %v", argv, err)
		}
	}
	for _, argv := range [][]string{
		{"kubectl", "delete", "pod", "x"},
		{"kubectl", "apply", "-f", "x.yaml"},
		{"kubectl", "edit", "deploy", "x"},
		{"kubectl", "scale", "deploy", "x", "--replicas=0"},
	} {
		if err := p.Check(argv); err == nil {
			t.Errorf("expected rejected %v, got nil", argv)
		}
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/access/ -run TestPolicy -v`
Expected: FAIL — `undefined: DefaultPolicy`

- [ ] **Step 3: 实现**

`pkg/doctor/access/policy.go`：

```go
package access

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Policy 只读策略。在命令真正执行前校验 argv，未登记的组合一律拒绝。
// 这是「硬只读」的执行点：工具层即使写错，也无法在此通过。
type Policy struct {
	binaryAllow map[string]bool
	cephVerbs   map[string]bool // "osd tree" 这类两级子命令
	cephDeny    map[string]bool // 显式的变更动词
	pathPrefix  []string        // 允许读取的路径前缀
}

// DefaultPolicy 返回默认只读策略。
func DefaultPolicy() *Policy {
	return &Policy{
		binaryAllow: map[string]bool{
			"ceph": true, "crictl": true, "docker": true, "kubectl": true,
			"iostat": true, "smartctl": true, "dmesg": true, "df": true,
			"free": true, "uptime": true, "lsblk": true, "tail": true, "grep": true,
			"ls": true, "command": true,
		},
		cephVerbs: map[string]bool{
			"-s": true, "--version": true, "-v": true,
			"status": true, "health": true, "df": true, "versions": true,
			"log": true, "tell": true, "daemon": true,
			"osd tree": true, "osd df": true, "osd metadata": true,
			"osd ok-to-stop": true, "osd ls": true, "osd pool autoscale-status": true,
			"pg stat": true, "pg dump_stuck": true,
			"mon stat": true, "mgr stat": true,
			"config get": true,
		},
		cephDeny: map[string]bool{
			"osd out": true, "osd in": true, "osd rm": true, "osd crush": true,
			"osd pool create": true, "osd pool set": true, "osd pool delete": true,
			"osd set": true, "osd unset": true, "config set": true, "config rm": true,
			"injectargs": true, "pg scrub": true, "pg repair": true, "pg deep-scrub": true,
		},
		pathPrefix: []string{"/var/log/ceph", "/var/lib/rook/rook-ceph/logs", "/var/lib/rook/rook-ceph"},
	}
}

// Check 校验一条 argv 是否允许执行。
func (p *Policy) Check(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("rejected: empty argv")
	}
	bin := argv[0]
	if !p.binaryAllow[bin] {
		return fmt.Errorf("rejected: binary %q not in allowlist", bin)
	}
	switch bin {
	case "ceph":
		return p.checkCeph(argv[1:])
	case "kubectl":
		return p.checkKubectl(argv[1:])
	case "crictl", "docker":
		return p.checkContainerCLI(argv[1:])
	case "tail":
		return p.checkTail(argv[1:])
	case "grep":
		return p.checkGrep(argv[1:])
	case "smartctl":
		return p.checkSmartctl(argv[1:])
	case "ls":
		return p.checkLs(argv[1:])
	case "command":
		return p.checkCommand(argv[1:])
	}
	return nil
}

// checkLs 只允许 ls -d <path>，且路径在白名单内。
func (p *Policy) checkLs(args []string) error {
	if len(args) != 2 || args[0] != "-d" {
		return fmt.Errorf("rejected: ls must be called as -d <path>")
	}
	if !p.pathAllowed(args[1]) {
		return fmt.Errorf("rejected: path %q outside allowed prefixes", args[1])
	}
	return nil
}

// checkCommand 只允许 command -v <固定的几个探测目标>。
func (p *Policy) checkCommand(args []string) error {
	if len(args) != 2 || args[0] != "-v" {
		return fmt.Errorf("rejected: command must be called as -v <tool>")
	}
	switch args[1] {
	case "iostat", "smartctl", "lsblk", "dmesg", "ceph", "kubectl", "crictl", "docker":
		return nil
	}
	return fmt.Errorf("rejected: command -v %q not allowed", args[1])
}

func (p *Policy) checkCeph(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("rejected: bare ceph")
	}
	// tell 的形态是 tell <daemon> <cmd>...，只允许只读 cmd
	if args[0] == "tell" {
		if len(args) < 3 {
			return fmt.Errorf("rejected: ceph tell needs daemon and command")
		}
		if p.cephDeny[args[2]] {
			return fmt.Errorf("rejected: ceph tell %q is a mutating command", args[2])
		}
		switch args[2] {
		case "dump_historic_ops", "dump_ops_in_flight", "status", "log", "version", "perf", "dump_blocked_ops":
			return nil
		}
		return fmt.Errorf("rejected: ceph tell command %q not in allowlist", args[2])
	}
	// pg <pgid> query 形态
	if args[0] == "pg" && len(args) >= 3 && args[2] == "query" {
		return nil
	}
	// 先试两级
	if len(args) >= 2 {
		two := args[0] + " " + args[1]
		if p.cephDeny[two] {
			return fmt.Errorf("rejected: ceph %q is a mutating command", two)
		}
		if p.cephVerbs[two] {
			return nil
		}
	}
	if p.cephDeny[args[0]] {
		return fmt.Errorf("rejected: ceph %q is a mutating command", args[0])
	}
	if p.cephVerbs[args[0]] {
		return nil
	}
	return fmt.Errorf("rejected: ceph subcommand %q not in allowlist", strings.Join(args[:min(2, len(args))], " "))
}

func (p *Policy) checkKubectl(args []string) error {
	verb := ""
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			verb = a
			break
		}
	}
	switch verb {
	case "get", "describe", "logs", "exec":
		return nil
	}
	return fmt.Errorf("rejected: kubectl verb %q not allowed", verb)
}

func (p *Policy) checkContainerCLI(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("rejected: bare container CLI")
	}
	switch args[0] {
	case "ps", "inspect", "logs":
		return nil
	case "exec":
		if len(args) < 3 {
			return fmt.Errorf("rejected: crictl/docker exec needs container and command")
		}
		return nil
	}
	return fmt.Errorf("rejected: container subcommand %q not allowed", args[0])
}

func (p *Policy) checkTail(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("rejected: tail needs a path")
	}
	path := args[len(args)-1]
	if !p.pathAllowed(path) {
		return fmt.Errorf("rejected: path %q outside allowed prefixes", path)
	}
	return nil
}

func (p *Policy) checkGrep(args []string) error {
	// 只允许 grep -F -c <pattern> <path> 形态
	if len(args) < 3 {
		return fmt.Errorf("rejected: grep needs flags, pattern and path")
	}
	if args[0] != "-F" || args[1] != "-c" {
		return fmt.Errorf("rejected: grep must be called as -F -c")
	}
	pattern := args[2]
	if strings.HasPrefix(pattern, "-") {
		return fmt.Errorf("rejected: grep pattern must not start with '-'")
	}
	if strings.ContainsAny(pattern, "\n\r\x00") {
		return fmt.Errorf("rejected: grep pattern contains control characters")
	}
	path := args[len(args)-1]
	if !p.pathAllowed(path) {
		return fmt.Errorf("rejected: path %q outside allowed prefixes", path)
	}
	return nil
}

func (p *Policy) checkSmartctl(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("rejected: smartctl needs flag and device")
	}
	switch args[0] {
	case "-a", "-H", "-i":
	default:
		return fmt.Errorf("rejected: smartctl flag %q not allowed", args[0])
	}
	if !strings.HasPrefix(args[1], "/dev/") {
		return fmt.Errorf("rejected: smartctl target must be a /dev path")
	}
	return nil
}

// pathAllowed 判断路径是否落在白名单前缀内，并拒绝任何 .. 穿越。
func (p *Policy) pathAllowed(path string) bool {
	if strings.Contains(path, "..") {
		return false
	}
	clean := filepath.Clean(path)
	for _, prefix := range p.pathPrefix {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/doctor/access/ -run TestPolicy -v`
Expected: PASS（8 个测试全部通过）

- [ ] **Step 5: 提交**

```bash
git add pkg/doctor/access/policy.go pkg/doctor/access/policy_test.go
git commit -m "feat(doctor/access): add read-only command allowlist policy"
```

---

### Task 3: 输出类型与错误分类

**Files:**
- Create: `pkg/doctor/access/output.go`
- Test: `pkg/doctor/access/output_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/access/output_test.go`：

```go
package access

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTruncateOutput(t *testing.T) {
	big := strings.Repeat("a", 1000) + strings.Repeat("b", 1000)
	got, truncated := truncateOutput(big, 600)
	if !truncated {
		t.Fatal("expected truncated")
	}
	if len(got) > 700 {
		t.Errorf("truncated output too large: %d", len(got))
	}
	if !strings.HasPrefix(got, "a") || !strings.HasSuffix(got, "b") {
		t.Error("expected head and tail preserved")
	}
	if !strings.Contains(got, "truncated") {
		t.Error("expected truncation marker")
	}
}

func TestTruncateOutputUnderLimit(t *testing.T) {
	got, truncated := truncateOutput("short", 100)
	if truncated {
		t.Fatal("did not expect truncation")
	}
	if got != "short" {
		t.Errorf("got %q", got)
	}
}

func TestClassifyTimeout(t *testing.T) {
	err := classifyError(errDeadline, "", 0)
	if err.Kind != KindTimeout {
		t.Errorf("kind = %v, want timeout", err.Kind)
	}
	if !strings.Contains(err.Message, "无响应") {
		t.Errorf("message should explain timeout in domain terms: %q", err.Message)
	}
}

func TestClassifyExitCode(t *testing.T) {
	err := classifyError(nil, "Error: monclient hunting", 1)
	if err.Kind != KindFailed {
		t.Errorf("kind = %v, want failed", err.Kind)
	}
	if !strings.Contains(err.Message, "monclient hunting") {
		t.Errorf("message should include stderr: %q", err.Message)
	}
}

func TestClassifyUnreachable(t *testing.T) {
	err := classifyError(errors.New("dial tcp 10.0.0.1:22: connect: connection refused"), "", 0)
	if err.Kind != KindUnreachable {
		t.Errorf("kind = %v, want unreachable", err.Kind)
	}
}

func TestClassifyEmptyIsNotError(t *testing.T) {
	if err := classifyError(nil, "", 0); err != nil {
		t.Fatalf("empty successful output should not be an error, got %v", err)
	}
}

func TestOutputCarriesDuration(t *testing.T) {
	o := Output{Stdout: "x", Duration: 3 * time.Second}
	if o.Duration != 3*time.Second {
		t.Error("duration not preserved")
	}
}

func TestToolErrorKindsDistinct(t *testing.T) {
	kinds := []ErrorKind{KindTimeout, KindUnreachable, KindUnavailable, KindFailed}
	seen := map[ErrorKind]bool{}
	for _, k := range kinds {
		if seen[k] {
			t.Errorf("duplicate kind %v", k)
		}
		seen[k] = true
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/access/ -run 'TestTruncate|TestClassify|TestOutput|TestToolError' -v`
Expected: FAIL — `undefined: truncateOutput`

- [ ] **Step 3: 实现**

`pkg/doctor/access/output.go`：

```go
package access

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Output 是一次远程命令执行的结果。
type Output struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
	Node     string
	Command  string // 实际执行的命令串（已脱敏，用于审计与证据）
	Truncated bool
}

// ErrorKind 区分远程执行失败的语义，模型据此决定下一步。
type ErrorKind string

const (
	KindTimeout     ErrorKind = "timeout"     // 命令未在时限内返回，通常意味着组件无响应
	KindUnreachable ErrorKind = "unreachable" // 节点/服务不可达
	KindUnavailable ErrorKind = "unavailable" // 环境不具备该能力
	KindFailed      ErrorKind = "failed"      // 命令执行但返回非零
)

// ToolError 是带语义分类的执行错误。
type ToolError struct {
	Kind    ErrorKind
	Message string
}

func (e *ToolError) Error() string { return string(e.Kind) + ": " + e.Message }

var errDeadline = context.DeadlineExceeded

// classifyError 把底层错误转成带语义的 ToolError。
func classifyError(err error, stderr string, exitCode int) *ToolError {
	if err == nil {
		if exitCode != 0 {
			return &ToolError{Kind: KindFailed, Message: firstNonEmpty(stderr, fmt.Sprintf("exit code %d", exitCode))}
		}
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errDeadline) {
		return &ToolError{
			Kind:    KindTimeout,
			Message: "命令在时限内未返回，通常意味着对应组件无响应",
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "connection timed out") ||
		strings.Contains(msg, "ssh: handshake failed") {
		return &ToolError{Kind: KindUnreachable, Message: msg}
	}
	if stderr != "" {
		return &ToolError{Kind: KindFailed, Message: stderr}
	}
	return &ToolError{Kind: KindFailed, Message: msg}
}

// unavailable 构造一个能力缺失错误。
func unavailable(format string, args ...any) *ToolError {
	return &ToolError{Kind: KindUnavailable, Message: fmt.Sprintf(format, args...)}
}

// truncateOutput 保留头尾、中间省略，避免超大输出冲爆上下文。
func truncateOutput(s string, limit int) (string, bool) {
	if limit <= 0 || len(s) <= limit {
		return s, false
	}
	half := limit / 2
	head := s[:half]
	tail := s[len(s)-half:]
	omitted := len(s) - len(head) - len(tail)
	return head + fmt.Sprintf("\n... [truncated %d bytes] ...\n", omitted) + tail, true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/doctor/access/ -run 'TestTruncate|TestClassify|TestOutput|TestToolError' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/doctor/access/output.go pkg/doctor/access/output_test.go
git commit -m "feat(doctor/access): add output type with semantic error classification"
```

---

### Task 4: 链式 SSH 建连

**Files:**
- Create: `pkg/doctor/access/config.go`
- Create: `pkg/doctor/access/dial.go`
- Test: `pkg/doctor/access/config_test.go`
- Test: `pkg/doctor/access/dial_test.go`

- [ ] **Step 1: 写 config 失败测试**

`pkg/doctor/access/config_test.go`：

```go
package access

import (
	"testing"
	"time"
)

func TestConfigDefaults(t *testing.T) {
	c := Config{Host: "10.20.30.1"}
	c.applyDefaults()
	if c.Port != 22 {
		t.Errorf("Port = %d, want 22", c.Port)
	}
	if c.User != "root" {
		t.Errorf("User = %q, want root", c.User)
	}
	if c.CommandTimeout != 30*time.Second {
		t.Errorf("CommandTimeout = %v", c.CommandTimeout)
	}
	if c.MaxOutputBytes != 262144 {
		t.Errorf("MaxOutputBytes = %d", c.MaxOutputBytes)
	}
	if c.MaxConcurrency != 8 {
		t.Errorf("MaxConcurrency = %d", c.MaxConcurrency)
	}
	if c.Policy == nil {
		t.Error("Policy should default to DefaultPolicy()")
	}
}

func TestConfigValidateRequiresHost(t *testing.T) {
	c := Config{PrivateKeyPath: "/tmp/k"}
	c.applyDefaults()
	if err := c.validate(); err == nil {
		t.Fatal("expected error for missing host")
	}
}

func TestConfigValidateRequiresAuth(t *testing.T) {
	c := Config{Host: "10.0.0.1"}
	c.applyDefaults()
	if err := c.validate(); err == nil {
		t.Fatal("expected error when neither key nor password provided")
	}
}

func TestConfigValidateAcceptsPrivateKey(t *testing.T) {
	c := Config{Host: "10.0.0.1", PrivateKeyPath: "/tmp/k"}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/access/ -run TestConfig -v`
Expected: FAIL — `undefined: Config`

- [ ] **Step 3: 实现 config.go**

`pkg/doctor/access/config.go`：

```go
package access

import (
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
)

// Config 接入层配置。
type Config struct {
	Host           string
	Port           int
	User           string
	Password       string
	PrivateKeyPath string
	KnownHostsPath string
	CommandTimeout time.Duration
	DialTimeout    time.Duration
	MaxOutputBytes int
	MaxConcurrency int
	Policy         *Policy

	// authMethods 由 Dial 内部填充，供链式连接复用
	authMethods []ssh.AuthMethod
}

func (c *Config) applyDefaults() {
	if c.Port == 0 {
		c.Port = 22
	}
	if c.User == "" {
		c.User = "root"
	}
	if c.CommandTimeout == 0 {
		c.CommandTimeout = 30 * time.Second
	}
	if c.DialTimeout == 0 {
		c.DialTimeout = 10 * time.Second
	}
	if c.MaxOutputBytes == 0 {
		c.MaxOutputBytes = 256 * 1024
	}
	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = 8
	}
	if c.Policy == nil {
		c.Policy = DefaultPolicy()
	}
}

func (c *Config) validate() error {
	if c.Host == "" {
		return fmt.Errorf("ssh host is required")
	}
	if c.PrivateKeyPath == "" && c.Password == "" {
		return fmt.Errorf("one of private_key or password is required")
	}
	return nil
}
```

- [ ] **Step 4: 运行 config 测试确认通过**

Run: `go test ./pkg/doctor/access/ -run TestConfig -v`
Expected: PASS

- [ ] **Step 5: 写 dial 失败测试**

`pkg/doctor/access/dial_test.go`：

```go
package access

import (
	"context"
	"testing"
	"time"
)

func TestChainAddr(t *testing.T) {
	if got := chainAddr("10.20.30.7", 22); got != "10.20.30.7:22" {
		t.Errorf("chainAddr = %q", got)
	}
	if got := chainAddr("10.20.30.7", 2222); got != "10.20.30.7:2222" {
		t.Errorf("chainAddr = %q", got)
	}
}

func TestDialFailsFastOnBadHost(t *testing.T) {
	c := Config{
		Host:           "127.0.0.1",
		Port:           1, // 不会有服务监听
		User:           "root",
		Password:       "x",
		CommandTimeout: 2 * time.Second,
		DialTimeout:    1 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := Dial(ctx, c)
	if err == nil {
		t.Fatal("expected dial error")
	}
}

func TestDialFailsWithoutAuth(t *testing.T) {
	c := Config{Host: "10.0.0.1"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := Dial(ctx, c); err == nil {
		t.Fatal("expected validation error")
	}
}
```

- [ ] **Step 6: 运行测试确认失败**

Run: `go test ./pkg/doctor/access/ -run 'TestChainAddr|TestDial' -v`
Expected: FAIL — `undefined: Dial`

- [ ] **Step 7: 实现 dial.go**

`pkg/doctor/access/dial.go`：

```go
package access

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"

	"golang.org/x/crypto/ssh"
)

// clusterConn 持有到首节点的连接，并由它派生子连接。
type clusterConn struct {
	entry *ssh.Client
	cfg   Config
}

// chainAddr 拼装节点的 SSH 地址。
func chainAddr(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// buildAuth 组装认证方式。
func buildAuth(cfg Config) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if cfg.PrivateKeyPath != "" {
		key, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("read private key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("no auth method configured")
	}
	return methods, nil
}

// Dial 建立到首节点的连接。其余节点通过 clusterConn.dialNode 派生。
func Dial(ctx context.Context, cfg Config) (*Cluster, error) {
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	auth, err := buildAuth(cfg)
	if err != nil {
		return nil, err
	}
	cfg.authMethods = auth

	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         cfg.DialTimeout,
	}

	type result struct {
		conn *ssh.Client
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, derr := ssh.Dial("tcp", chainAddr(cfg.Host, cfg.Port), clientCfg)
		ch <- result{c, derr}
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("dial %s: %w", cfg.Host, ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("dial %s: %w", cfg.Host, r.err)
		}
		return newCluster(&clusterConn{entry: r.conn, cfg: cfg}), nil
	}
}

// dialNode 经首节点建立到目标节点的连接，复用首节点的免密配置。
// 首节点自身直接复用 entry。
func (cc *clusterConn) dialNode(ctx context.Context, host string) (*ssh.Client, error) {
	if host == cc.cfg.Host {
		return cc.entry, nil
	}

	clientCfg := &ssh.ClientConfig{
		User:            cc.cfg.User,
		Auth:            cc.cfg.authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         cc.cfg.DialTimeout,
	}

	type result struct {
		conn *ssh.Client
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		raw, derr := cc.entry.Dial("tcp", chainAddr(host, cc.cfg.Port))
		if derr != nil {
			ch <- result{nil, derr}
			return
		}
		ncc, cerr := ssh.NewClientConn(raw, chainAddr(host, cc.cfg.Port), clientCfg)
		if cerr != nil {
			raw.Close()
			ch <- result{nil, cerr}
			return
		}
		ch <- result{ssh.NewClient(ncc, raw), nil}
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("chain to %s: %w", host, ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("chain to %s: %w", host, r.err)
		}
		return r.conn, nil
	}
}

func (cc *clusterConn) Close() error {
	return cc.entry.Close()
}
```

- [ ] **Step 8: 运行测试（Cluster 尚未实现，先只跑 config）**

Run: `go test ./pkg/doctor/access/ -run TestConfig -v`
Expected: PASS

Task 7 会实现 `newCluster` 并让 dial 测试跑通。

- [ ] **Step 9: 提交**

```bash
git add pkg/doctor/access/config.go pkg/doctor/access/config_test.go pkg/doctor/access/dial.go pkg/doctor/access/dial_test.go
git commit -m "feat(doctor/access): add chain-SSH dialing with default config"
```

---

### Task 5: 能力探测

**Files:**
- Create: `pkg/doctor/access/probe.go`
- Test: `pkg/doctor/access/probe_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/access/fake_transport_test.go`（探测测试用的共享假实现）：

```go
package access

import (
	"context"
	"strings"
)

type fakeResp struct {
	stdout string
	stderr string
	exit   int
}

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
```

`pkg/doctor/access/probe_test.go`：

```go
package access

import (
	"context"
	"testing"
)

func TestProbePicksHostCephFirst(t *testing.T) {
	tr := &fakeTransport{
		responses: map[string]fakeResp{
			"ceph --version": {stdout: "ceph version 18.2.4 (e7c...) reef (stable)\n"},
		},
	}
	caps := probeCapabilities(context.Background(), tr)
	if !caps.CephCLI {
		t.Fatal("expected CephCLI true")
	}
	if caps.Backend != "host-ceph" {
		t.Errorf("Backend = %q, want host-ceph", caps.Backend)
	}
	if caps.CephVersion != "18.2.4" {
		t.Errorf("CephVersion = %q", caps.CephVersion)
	}
}

func TestProbeFallsBackToToolbox(t *testing.T) {
	tr := &fakeTransport{
		responses: map[string]fakeResp{
			"ceph --version":                      {exit: 127, stderr: "command not found"},
			"kubectl get pod -n rook-ceph -l app=rook-ceph-tools": {stdout: "rook-ceph-tools-abc   1/1   Running\n"},
		},
	}
	caps := probeCapabilities(context.Background(), tr)
	if !caps.CephCLI {
		t.Fatal("expected CephCLI true via toolbox")
	}
	if caps.Backend != "toolbox" {
		t.Errorf("Backend = %q, want toolbox", caps.Backend)
	}
}

func TestProbeFallsBackToContainerExec(t *testing.T) {
	tr := &fakeTransport{
		responses: map[string]fakeResp{
			"ceph --version":                      {exit: 127},
			"kubectl get pod -n rook-ceph -l app=rook-ceph-tools": {exit: 1},
			"crictl ps --name rook-ceph-mon -q":   {stdout: "abc123\n"},
		},
	}
	caps := probeCapabilities(context.Background(), tr)
	if caps.Backend != "container-exec" {
		t.Errorf("Backend = %q, want container-exec", caps.Backend)
	}
	if caps.ContainerRT != "crictl" {
		t.Errorf("ContainerRT = %q", caps.ContainerRT)
	}
}

func TestProbeFallsBackToLogOnly(t *testing.T) {
	tr := &fakeTransport{
		responses: map[string]fakeResp{
			"ceph --version":                      {exit: 127},
			"kubectl get pod -n rook-ceph -l app=rook-ceph-tools": {exit: 1},
			"crictl ps --name rook-ceph-mon -q":   {exit: 1},
			"docker ps --filter name=rook-ceph-mon -q": {exit: 127},
			"ls -d /var/lib/rook/rook-ceph/logs":  {stdout: "/var/lib/rook/rook-ceph/logs\n"},
		},
	}
	caps := probeCapabilities(context.Background(), tr)
	if caps.Backend != "log-only" {
		t.Errorf("Backend = %q, want log-only", caps.Backend)
	}
	if !caps.CephCLI {
		// log-only 时 CephCLI 必须为 false，模型据此改走日志路径
	} else {
		t.Error("CephCLI should be false in log-only mode")
	}
	if len(caps.LogDirs) == 0 {
		t.Error("expected LogDirs populated")
	}
}

func TestProbeParsesVersion(t *testing.T) {
	cases := map[string]string{
		"ceph version 18.2.4 (abc) reef (stable)":           "18.2.4",
		"ceph version 17.2.7 (def) quincy (stable)":         "17.2.7",
		"ceph version 16.2.15 (ghi) pacific (stable)":       "16.2.15",
		"garbage":                                           "",
	}
	for in, want := range cases {
		if got := parseCephVersion(in); got != want {
			t.Errorf("parseCephVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProbeHostTools(t *testing.T) {
	tr := &fakeTransport{
		responses: map[string]fakeResp{
			"ceph --version":  {stdout: "ceph version 18.2.4 (x) reef (stable)"},
			"command -v iostat": {stdout: "/usr/bin/iostat\n"},
			"command -v smartctl": {exit: 1},
			"command -v lsblk": {stdout: "/usr/bin/lsblk\n"},
		},
	}
	caps := probeCapabilities(context.Background(), tr)
	if !caps.HostTools["iostat"] {
		t.Error("iostat should be available")
	}
	if caps.HostTools["smartctl"] {
		t.Error("smartctl should be unavailable")
	}
	if !caps.HostTools["lsblk"] {
		t.Error("lsblk should be available")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/access/ -run TestProbe -v`
Expected: FAIL — `undefined: probeCapabilities`

- [ ] **Step 3: 实现**

`pkg/doctor/access/probe.go`：

```go
package access

import (
	"context"
	"regexp"
	"strings"
)

// Capabilities 描述某节点实际具备的能力。
type Capabilities struct {
	CephCLI     bool
	CephVersion string
	Backend     string // host-ceph / toolbox / container-exec / log-only
	Kubectl     bool
	ContainerRT string // crictl / docker / none
	LogDirs     []string
	HostTools   map[string]bool
}

// transport 是探测需要的执行能力，便于测试注入。
type transport interface {
	exec(ctx context.Context, argv []string) (string, string, int)
}

var versionRe = regexp.MustCompile(`ceph version (\d+\.\d+\.\d+)`)

func parseCephVersion(s string) string {
	m := versionRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

var defaultLogDirs = []string{
	"/var/log/ceph",
	"/var/lib/rook/rook-ceph/logs",
}

// probeCapabilities 按 host-ceph → toolbox → container-exec → log-only 顺序探测。
func probeCapabilities(ctx context.Context, tr transport) Capabilities {
	caps := Capabilities{HostTools: map[string]bool{}}

	// 1. 宿主机 ceph 客户端
	if out, _, code := tr.exec(ctx, []string{"ceph", "--version"}); code == 0 {
		if v := parseCephVersion(out); v != "" {
			caps.CephCLI = true
			caps.CephVersion = v
			caps.Backend = "host-ceph"
		}
	}

	// 2. kubectl + toolbox pod
	if out, _, code := tr.exec(ctx, []string{"kubectl", "get", "pod", "-n", "rook-ceph", "-l", "app=rook-ceph-tools"}); code == 0 {
		caps.Kubectl = true
		if !caps.CephCLI && strings.Contains(out, "Running") {
			caps.CephCLI = true
			caps.Backend = "toolbox"
		}
	}

	// 3. 容器运行时
	if !caps.CephCLI {
		if out, _, code := tr.exec(ctx, []string{"crictl", "ps", "--name", "rook-ceph-mon", "-q"}); code == 0 && strings.TrimSpace(out) != "" {
			caps.CephCLI = true
			caps.Backend = "container-exec"
			caps.ContainerRT = "crictl"
		} else if out, _, code := tr.exec(ctx, []string{"docker", "ps", "--filter", "name=rook-ceph-mon", "-q"}); code == 0 && strings.TrimSpace(out) != "" {
			caps.CephCLI = true
			caps.Backend = "container-exec"
			caps.ContainerRT = "docker"
		}
	}

	// 4. 日志目录（总是探测，log-only 时是唯一数据源）
	for _, dir := range defaultLogDirs {
		if out, _, code := tr.exec(ctx, []string{"ls", "-d", dir}); code == 0 && strings.TrimSpace(out) != "" {
			caps.LogDirs = append(caps.LogDirs, dir)
		}
	}
	if caps.Backend == "" {
		caps.Backend = "log-only"
	}

	// 5. 宿主机工具
	for _, tool := range []string{"iostat", "smartctl", "lsblk", "dmesg"} {
		_, _, code := tr.exec(ctx, []string{"command", "-v", tool})
		caps.HostTools[tool] = code == 0
	}

	return caps
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/doctor/access/ -run TestProbe -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/doctor/access/probe.go pkg/doctor/access/probe_test.go
git commit -m "feat(doctor/access): add capability probing with graceful degradation"
```

---

### Task 6: 拓扑发现与设备解析

**Files:**
- Create: `pkg/doctor/access/topology.go`
- Test: `pkg/doctor/access/topology_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/access/topology_test.go`：

```go
package access

import (
	"context"
	"testing"
)

const osdTreeJSON = `{
  "nodes": [
    {"id": -1, "name": "default", "type": "root", "children": [-3]},
    {"id": -3, "name": "node-3", "type": "host", "children": [7, 8]},
    {"id": 7, "name": "osd.7", "type": "osd", "status": "up", "host": "node-3"},
    {"id": 8, "name": "osd.8", "type": "osd", "status": "down", "host": "node-3"}
  ],
  "strays": []
}`

const osdMetadata7JSON = `{
  "id": 7,
  "hostname": "node-3",
  "bluestore_bdev_devices": "sdc",
  "devices": "sdc",
  "osd_objectstore": "bluestore"
}`

const osdMetadataLVMJSON = `{
  "id": 9,
  "hostname": "node-4",
  "bluestore_bdev_devices": "/dev/mapper/ceph--9-abc-lv--block",
  "devices": "/dev/mapper/ceph--9-abc-lv--block",
  "osd_objectstore": "bluestore"
}`

func TestParseOSDTree(t *testing.T) {
	nodes, err := parseOSDTree(osdTreeJSON)
	if err != nil {
		t.Fatal(err)
	}
	// 只取 osd 类型的节点
	if len(nodes) != 2 {
		t.Fatalf("got %d osd nodes, want 2", len(nodes))
	}
	if nodes[0].ID != 7 || nodes[0].Host != "node-3" || nodes[0].Status != "up" {
		t.Errorf("unexpected first node: %+v", nodes[0])
	}
	if nodes[1].Status != "down" {
		t.Errorf("expected second node down, got %q", nodes[1].Status)
	}
}

func TestParseOSDMetadataRawDevice(t *testing.T) {
	p, err := parseOSDMetadata(7, osdMetadata7JSON)
	if err != nil {
		t.Fatal(err)
	}
	if p.Host != "node-3" {
		t.Errorf("Host = %q", p.Host)
	}
	if p.Device != "sdc" {
		t.Errorf("Device = %q", p.Device)
	}
	if p.RawDevice != "sdc" {
		t.Errorf("RawDevice = %q", p.RawDevice)
	}
}

func TestParseOSDMetadataLVMDevice(t *testing.T) {
	p, err := parseOSDMetadata(9, osdMetadataLVMJSON)
	if err != nil {
		t.Fatal(err)
	}
	// LVM 路径保留在 RawDevice，Device 需由 lsblk 反查后再填
	if p.RawDevice != "/dev/mapper/ceph--9-abc-lv--block" {
		t.Errorf("RawDevice = %q", p.RawDevice)
	}
	if !isLVMPath(p.RawDevice) {
		t.Error("expected LVM path detection")
	}
}

func TestIsLVMPath(t *testing.T) {
	cases := map[string]bool{
		"/dev/mapper/ceph--9-abc-lv--block": true,
		"/dev/ceph-abc/osd-block-xyz":       true,
		"sdc":                               false,
		"/dev/sdb":                          false,
	}
	for in, want := range cases {
		if got := isLVMPath(in); got != want {
			t.Errorf("isLVMPath(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseLsblkParent(t *testing.T) {
	// lsblk -no PKNAME 对 LVM 设备返回其所属物理盘
	if got := parseLsblkParent("sda\n"); got != "sda" {
		t.Errorf("got %q", got)
	}
	if got := parseLsblkParent("\n"); got != "" {
		t.Errorf("got %q", got)
	}
	if got := parseLsblkParent("sda\nsdb\n"); got != "sda" {
		t.Errorf("got %q", got)
	}
}

func TestBuildTopologyUsesMetadata(t *testing.T) {
	tr := &fakeTransport{
		responses: map[string]fakeResp{
			"ceph osd metadata 7 -f json": {stdout: osdMetadata7JSON},
			"ceph osd metadata 8 -f json": {stdout: `{"id":8,"hostname":"node-3","bluestore_bdev_devices":"sdd"}`},
		},
	}
	nodes := []osdNode{
		{ID: 7, Host: "node-3", Status: "up"},
		{ID: 8, Host: "node-3", Status: "down"},
	}
	topo := buildTopology(context.Background(), tr, "fsid-1", nodes)
	if topo.FSID != "fsid-1" {
		t.Errorf("FSID = %q", topo.FSID)
	}
	if len(topo.OSDs) != 2 {
		t.Fatalf("got %d placements", len(topo.OSDs))
	}
	if topo.OSDs[0].Device != "sdc" {
		t.Errorf("first device = %q", topo.OSDs[0].Device)
	}
	if len(topo.Nodes) != 1 || topo.Nodes[0].Name != "node-3" {
		t.Errorf("expected node-3 deduped, got %+v", topo.Nodes)
	}
	if len(topo.Nodes[0].OSDIDs) != 2 {
		t.Errorf("expected node-3 to hold 2 osds, got %v", topo.Nodes[0].OSDIDs)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/access/ -run 'TestParseOSD|TestIsLVM|TestParseLsblk|TestBuildTopology' -v`
Expected: FAIL — `undefined: parseOSDTree`

- [ ] **Step 3: 实现**

`pkg/doctor/access/topology.go`：

```go
package access

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Topology 是集群的节点与 OSD→设备映射。
type Topology struct {
	FSID  string
	Nodes []TopoNode
	OSDs  []OSDPlacement
}

// TopoNode 是集群中的一个节点。
type TopoNode struct {
	Name   string
	Host   string
	OSDIDs []int
	Roles  []string
}

// OSDPlacement 描述单个 OSD 的物理位置。
type OSDPlacement struct {
	ID        int
	Host      string
	Device    string // 解析后的物理盘，如 sdc
	RawDevice string // 原始值，可能是 LVM 路径
	DBDevice  string
	Status    string
}

// osdNode 是 ceph osd tree 里的 OSD 条目。
type osdNode struct {
	ID     int
	Host   string
	Status string
}

type rawOSDTree struct {
	Nodes []struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Type   string `json:"type"`
		Status string `json:"status"`
		Host   string `json:"host"`
	} `json:"nodes"`
}

// parseOSDTree 从 ceph osd tree -f json 提取 OSD 节点。
func parseOSDTree(raw string) ([]osdNode, error) {
	var tree rawOSDTree
	if err := json.Unmarshal([]byte(raw), &tree); err != nil {
		return nil, fmt.Errorf("parse osd tree: %w", err)
	}
	var out []osdNode
	for _, n := range tree.Nodes {
		if n.Type != "osd" {
			continue
		}
		out = append(out, osdNode{ID: n.ID, Host: n.Host, Status: n.Status})
	}
	return out, nil
}

type rawOSDMetadata struct {
	ID             int    `json:"id"`
	Hostname       string `json:"hostname"`
	BlueStoreBDevs string `json:"bluestore_bdev_devices"`
	Devices        string `json:"devices"`
	ObjectStore    string `json:"osd_objectstore"`
}

// parseOSDMetadata 从 ceph osd metadata <id> -f json 提取物理位置。
func parseOSDMetadata(id int, raw string) (OSDPlacement, error) {
	var m rawOSDMetadata
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return OSDPlacement{}, fmt.Errorf("parse osd metadata %d: %w", id, err)
	}
	dev := m.BlueStoreBDevs
	if dev == "" {
		dev = m.Devices
	}
	p := OSDPlacement{
		ID:        id,
		Host:      m.Hostname,
		RawDevice: dev,
	}
	if !isLVMPath(dev) {
		p.Device = strings.TrimSpace(dev)
	}
	return p, nil
}

// isLVMPath 判断设备路径是否为 LVM 逻辑卷。
func isLVMPath(dev string) bool {
	return strings.HasPrefix(dev, "/dev/mapper/") ||
		(strings.HasPrefix(dev, "/dev/ceph-") && strings.Contains(dev, "/"))
}

// parseLsblkParent 解析 lsblk -no PKNAME 的输出，取第一行。
func parseLsblkParent(out string) string {
	for _, line := range strings.Split(out, "
") {
		if l := strings.TrimSpace(line); l != "" {
			return l
		}
	}
	return ""
}

// buildTopology 拉取所有 OSD 的 metadata，构建映射表。
func buildTopology(ctx context.Context, tr transport, fsid string, nodes []osdNode) Topology {
	topo := Topology{FSID: fsid}
	byHost := map[string]*TopoNode{}

	for _, n := range nodes {
		out, _, code := tr.exec(ctx, []string{"ceph", "osd", "metadata", fmt.Sprint(n.ID), "-f", "json"})
		if code != 0 {
			// metadata 拿不到时保留最小信息，设备留空
			topo.OSDs = append(topo.OSDs, OSDPlacement{ID: n.ID, Host: n.Host, Status: n.Status})
			byHost[n.Host] = ensureNode(byHost, n.Host)
			byHost[n.Host].OSDIDs = append(byHost[n.Host].OSDIDs, n.ID)
			continue
		}
		p, err := parseOSDMetadata(n.ID, out)
		if err != nil {
			p = OSDPlacement{ID: n.ID, Host: n.Host}
		}
		p.Status = n.Status
		if p.Host == "" {
			p.Host = n.Host
		}
		topo.OSDs = append(topo.OSDs, p)

		nd := ensureNode(byHost, p.Host)
		nd.OSDIDs = append(nd.OSDIDs, n.ID)
	}

	for _, nd := range byHost {
		topo.Nodes = append(topo.Nodes, *nd)
	}
	return topo
}

func ensureNode(m map[string]*TopoNode, host string) *TopoNode {
	if n, ok := m[host]; ok {
		return n
	}
	n := &TopoNode{Name: host, Host: host}
	m[host] = n
	return n
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/doctor/access/ -run 'TestParseOSD|TestIsLVM|TestParseLsblk|TestBuildTopology' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/doctor/access/topology.go pkg/doctor/access/topology_test.go
git commit -m "feat(doctor/access): add topology discovery and device path parsing"
```

---

### Task 7: Cluster 对外入口

**Files:**
- Create: `pkg/doctor/access/cluster.go`
- Test: `pkg/doctor/access/cluster_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/access/cluster_test.go`：

```go
package access

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeSession 模拟一次远程会话。
type fakeSession struct {
	stdout string
	stderr string
	err    error
	delay  time.Duration
}

func TestClusterRunRejectsPolicyViolation(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		return fakeSession{stdout: "x"}
	})
	_, err := c.Run(context.Background(), "node-3", []string{"ceph", "osd", "out", "7"})
	if err == nil {
		t.Fatal("expected policy rejection")
	}
	var te *ToolError
	if !errors.As(err, &te) || te.Kind != KindFailed {
		t.Errorf("expected KindFailed tool error, got %v", err)
	}
}

func TestClusterRunReturnsStdout(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		return fakeSession{stdout: "HEALTH_OK
"}
	})
	out, err := c.Run(context.Background(), "node-3", []string{"ceph", "-s"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Stdout != "HEALTH_OK
" {
		t.Errorf("stdout = %q", out.Stdout)
	}
	if out.Node != "node-3" {
		t.Errorf("node = %q", out.Node)
	}
	if out.Command == "" {
		t.Error("command should be recorded for audit")
	}
}

func TestClusterRunClassifiesTimeout(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		return fakeSession{err: context.DeadlineExceeded}
	})
	_, err := c.Run(context.Background(), "node-3", []string{"ceph", "-s"})
	var te *ToolError
	if !errors.As(err, &te) || te.Kind != KindTimeout {
		t.Fatalf("expected KindTimeout, got %v", err)
	}
}

func TestClusterRunClassifiesUnreachable(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		return fakeSession{err: errors.New("ssh: handshake failed: connection refused")}
	})
	_, err := c.Run(context.Background(), "node-3", []string{"ceph", "-s"})
	var te *ToolError
	if !errors.As(err, &te) || te.Kind != KindUnreachable {
		t.Fatalf("expected KindUnreachable, got %v", err)
	}
}

func TestClusterRunTruncatesLargeOutput(t *testing.T) {
	big := make([]byte, 400*1024)
	for i := range big {
		big[i] = 'a'
	}
	c := newTestCluster(t, func(argv []string) fakeSession {
		return fakeSession{stdout: string(big)}
	})
	out, err := c.Run(context.Background(), "node-3", []string{"ceph", "log", "last", "10000"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Truncated {
		t.Error("expected truncated")
	}
	if len(out.Stdout) > 300*1024 {
		t.Errorf("output too large after truncation: %d", len(out.Stdout))
	}
}

func TestClusterCachesCapabilities(t *testing.T) {
	calls := 0
	c := newTestCluster(t, func(argv []string) fakeSession {
		if len(argv) > 0 && argv[0] == "ceph" && len(argv) > 1 && argv[1] == "--version" {
			calls++
			return fakeSession{stdout: "ceph version 18.2.4 (x) reef (stable)"}
		}
		return fakeSession{exitNotOk()}
	})
	c.Capabilities(context.Background(), "node-3")
	c.Capabilities(context.Background(), "node-3")
	if calls != 1 {
		t.Errorf("capability probe ran %d times, want 1 (should cache)", calls)
	}
}

func TestClusterRejectsUnknownNode(t *testing.T) {
	c := newTestCluster(t, func(argv []string) fakeSession {
		return fakeSession{stdout: "x"}
	})
	_, err := c.Run(context.Background(), "node-unknown", []string{"ceph", "-s"})
	if err == nil {
		t.Fatal("expected error for unknown node")
	}
}

// —— 测试辅助 ——

func exitNotOk() fakeSession {
	return fakeSession{exit: 127}
}

func newTestCluster(t *testing.T, handler func(argv []string) fakeSession) *Cluster {
	t.Helper()
	cc := &clusterConn{cfg: Config{CommandTimeout: time.Second, MaxOutputBytes: 256 * 1024, Policy: DefaultPolicy()}}
	c := newCluster(cc)
	c.nodes["node-3"] = true
	c.setExecutor(func(ctx context.Context, node string, argv []string) (Output, error) {
		if !c.nodes[node] {
			return Output{}, unavailable("unknown node %q", node)
		}
		r := handler(argv)
		if r.err != nil {
			return Output{}, r.err
		}
		if r.exit != 0 {
			return Output{}, classifyError(nil, r.stderr, r.exit)
		}
		return Output{Stdout: r.stdout, Stderr: r.stderr, Node: node}, nil
	})
	return c
}
```

注意：`fakeSession` 需要 `exit` 字段，定义如下（放在测试文件顶部）：

```go
type fakeSession struct {
	stdout string
	stderr string
	exit   int
	err    error
	delay  time.Duration
}
```

第一个定义 `TestClusterRunRejectsPolicyViolation` 上方的结构体已包含全部字段，实施时以那份为准（`exit` 字段在其中）。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/access/ -run TestCluster -v`
Expected: FAIL — `undefined: Cluster`

- [ ] **Step 3: 实现**

`pkg/doctor/access/cluster.go`：

```go
package access

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// executor 是执行层的可替换实现，测试时注入假实现。
type executor func(ctx context.Context, node string, argv []string) (Output, error)

// Cluster 是接入层对外的唯一入口。
type Cluster struct {
	conn        *clusterConn
	nodes       map[string]bool
	caps        map[string]Capabilities
	executor    executor
	topo        *Topology
	clients     map[string]*ssh.Client
}

func newCluster(cc *clusterConn) *Cluster {
	c := &Cluster{
		conn:    cc,
		nodes:   map[string]bool{cc.cfg.Host: true},
		caps:    map[string]Capabilities{},
		clients: map[string]*ssh.Client{},
	}
	c.executor = c.realExecute
	return c
}

func (c *Cluster) setExecutor(e executor) { c.executor = e }

// Run 在指定节点上执行一条只读命令。
func (c *Cluster) Run(ctx context.Context, node string, argv []string) (Output, error) {
	if err := c.conn.cfg.Policy.Check(argv); err != nil {
		return Output{}, &ToolError{Kind: KindFailed, Message: err.Error()}
	}
	if !c.nodes[node] {
		return Output{}, unavailable("unknown node %q", node)
	}
	out, err := c.executor(ctx, node, argv)
	if err != nil {
		return out, err
	}
	out.Node = node
	out.Command = BuildCommand(argv)
	if trunc, did := truncateOutput(out.Stdout, c.conn.cfg.MaxOutputBytes); did {
		out.Stdout = trunc
		out.Truncated = true
	}
	return out, nil
}

// realExecute 通过链式 SSH 执行。
func (c *Cluster) realExecute(ctx context.Context, node string, argv []string) (Output, error) {
	client, err := c.clientFor(ctx, node)
	if err != nil {
		return Output{}, classifyError(err, "", 0)
	}

	session, err := client.NewSession()
	if err != nil {
		return Output{}, classifyError(err, "", 0)
	}
	defer session.Close()

	var stdout, stderr strings.Builder
	session.Stdout = &stdout
	session.Stderr = &stderr

	cmd := BuildCommand(argv)
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()

	timeout := c.conn.cfg.CommandTimeout
	select {
	case <-ctx.Done():
		session.Signal(ssh.SIGKILL)
		return Output{}, &ToolError{Kind: KindTimeout, Message: "命令在时限内未返回，通常意味着对应组件无响应"}
	case <-time.After(timeout):
		session.Signal(ssh.SIGKILL)
		return Output{}, &ToolError{Kind: KindTimeout, Message: "命令在时限内未返回，通常意味着对应组件无响应"}
	case err := <-done:
		out := Output{
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			Duration: time.Since(start),
			Node:     node,
			Command:  cmd,
		}
		if err != nil {
			var exitErr *ssh.ExitError
			if ok := asExitError(err, &exitErr); ok {
				out.ExitCode = exitErr.ExitStatus()
				return out, classifyError(nil, out.Stderr, out.ExitCode)
			}
			return out, classifyError(err, out.Stderr, 0)
		}
		return out, nil
	}
}

func asExitError(err error, target **ssh.ExitError) bool {
	if e, ok := err.(*ssh.ExitError); ok {
		*target = e
		return true
	}
	return false
}

func (c *Cluster) clientFor(ctx context.Context, node string) (*ssh.Client, error) {
	if cl, ok := c.clients[node]; ok {
		return cl, nil
	}
	cl, err := c.conn.dialNode(ctx, node)
	if err != nil {
		return nil, err
	}
	c.clients[node] = cl
	return cl, nil
}

// Capabilities 返回节点的能力，结果按节点缓存。
func (c *Cluster) Capabilities(ctx context.Context, node string) Capabilities {
	if caps, ok := c.caps[node]; ok {
		return caps
	}
	tr := &clusterTransport{cluster: c, node: node}
	caps := probeCapabilities(ctx, tr)
	c.caps[node] = caps
	return caps
}

// Topology 返回集群拓扑，首次调用时发现并缓存。
func (c *Cluster) Topology(ctx context.Context) (*Topology, error) {
	if c.topo != nil {
		return c.topo, nil
	}
	out, err := c.Run(ctx, c.conn.cfg.Host, []string{"ceph", "osd", "tree", "-f", "json"})
	if err != nil {
		return nil, err
	}
	nodes, err := parseOSDTree(out.Stdout)
	if err != nil {
		return nil, err
	}
	tr := &clusterTransport{cluster: c, node: c.conn.cfg.Host}
	topo := buildTopology(ctx, tr, "", nodes)

	// 拉取 FSID 并登记所有节点
	if st, err := c.Run(ctx, c.conn.cfg.Host, []string{"ceph", "fsid"}); err == nil {
		_ = st
	}
	for _, n := range topo.Nodes {
		c.nodes[n.Name] = true
	}
	c.topo = &topo
	return c.topo, nil
}

// Nodes 返回已知节点列表。
func (c *Cluster) Nodes() []string {
	out := make([]string, 0, len(c.nodes))
	for n := range c.nodes {
		out = append(out, n)
	}
	return out
}

// Close 关闭所有连接。
func (c *Cluster) Close() error {
	for _, cl := range c.clients {
		if cl != c.conn.entry {
			cl.Close()
		}
	}
	return c.conn.Close()
}

// clusterTransport 把 Cluster 适配成探测用的 transport。
type clusterTransport struct {
	cluster *Cluster
	node    string
}

func (t *clusterTransport) exec(ctx context.Context, argv []string) (string, string, int) {
	out, err := t.cluster.Run(ctx, t.node, argv)
	if err != nil {
		var te *ToolError
		if ok := errorsAs(err, &te); ok && te.Kind == KindFailed {
			return out.Stdout, out.Stderr, 1
		}
		return out.Stdout, out.Stderr, 1
	}
	return out.Stdout, out.Stderr, 0
}

func errorsAs(err error, target **ToolError) bool {
	if te, ok := err.(*ToolError); ok {
		*target = te
		return true
	}
	return false
}

var _ = fmt.Sprint
```

**实现注意**：
1. `ceph fsid` 未在白名单中，Task 2 需补 `"fsid": true` 到 `cephVerbs`。实施时一并加上，并在 `policy_test.go` 的 `TestPolicyAllowsReadOnlyCeph` 里增加 `{"ceph", "fsid"}` 用例。
2. `Topology` 中 `buildTopology` 传入的 fsid 参数在拿到 `ceph fsid` 输出后应回填；实现时先调用 `ceph fsid`，再 `buildTopology(ctx, tr, fsid, nodes)`。把 Step 3 中那段顺序调整为：先取 fsid，再解析树，再建拓扑。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/doctor/access/ -v`
Expected: PASS（access 层全部测试通过）

- [ ] **Step 5: 运行 race 检测**

Run: `go test -race ./pkg/doctor/access/`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add pkg/doctor/access/cluster.go pkg/doctor/access/cluster_test.go pkg/doctor/access/policy.go pkg/doctor/access/policy_test.go
git commit -m "feat(doctor/access): add Cluster entry point with policy, timeout and caching"
```

---

# M2：tools 层

### Task 8: 工具注册表与共享假 Runner

**Files:**
- Create: `pkg/doctor/tools/registry.go`
- Test: `pkg/doctor/tools/registry_test.go`
- Test: `pkg/doctor/tools/fake_runner_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/tools/fake_runner_test.go`：

```go
package tools

import (
	"context"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

// fakeRunner 用预置响应模拟集群，使工具层测试不依赖真实机器。
type fakeRunner struct {
	responses map[string]access.Output // key: "node|ceph osd df -f json"
	errs      map[string]error
	caps      access.Capabilities
	topo      *access.Topology
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		responses: map[string]access.Output{},
		errs:      map[string]error{},
		caps: access.Capabilities{
			CephCLI: true, CephVersion: "18.2.4", Backend: "host-ceph",
			Kubectl: false, ContainerRT: "none",
			LogDirs:  []string{"/var/log/ceph"},
			HostTools: map[string]bool{"iostat": true, "smartctl": true, "lsblk": true, "dmesg": true},
		},
		topo: &access.Topology{
			FSID: "fsid-abc",
			Nodes: []access.TopoNode{
				{Name: "node-3", Host: "node-3", OSDIDs: []int{7, 8}},
				{Name: "node-4", Host: "node-4", OSDIDs: []int{9}},
			},
			OSDs: []access.OSDPlacement{
				{ID: 7, Host: "node-3", Device: "sdc", Status: "up"},
				{ID: 8, Host: "node-3", Device: "sdd", Status: "up"},
				{ID: 9, Host: "node-4", Device: "sdb", Status: "up"},
			},
		},
	}
}

// on 注册某节点某命令的输出。
func (f *fakeRunner) on(node string, argv []string, stdout string) *fakeRunner {
	f.responses[node+"|"+strings.Join(argv, " ")] = access.Output{Stdout: stdout, Node: node}
	return f
}

// fail 注册某节点某命令的错误。
func (f *fakeRunner) fail(node string, argv []string, err error) *fakeRunner {
	f.errs[node+"|"+strings.Join(argv, " ")] = err
	return f
}

func (f *fakeRunner) Run(_ context.Context, node string, argv []string) (access.Output, error) {
	key := node + "|" + strings.Join(argv, " ")
	if err, ok := f.errs[key]; ok {
		return access.Output{}, err
	}
	if out, ok := f.responses[key]; ok {
		return out, nil
	}
	return access.Output{}, &access.ToolError{Kind: access.KindUnavailable, Message: "not stubbed: " + key}
}

func (f *fakeRunner) Capabilities(_ context.Context, _ string) access.Capabilities {
	return f.caps
}

func (f *fakeRunner) Topology(_ context.Context) (*access.Topology, error) {
	return f.topo, nil
}

func (f *fakeRunner) Nodes() []string {
	out := make([]string, 0, len(f.topo.Nodes))
	for _, n := range f.topo.Nodes {
		out = append(out, n.Name)
	}
	return out
}
```

`pkg/doctor/tools/registry_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultRegistryHasTwelveTools(t *testing.T) {
	r := DefaultRegistry(nil)
	names := r.Names()
	if len(names) != 12 {
		t.Fatalf("got %d tools, want 12: %v", len(names), names)
	}
	want := []string{
		"ceph_status", "osd_tree", "pool_info", "pg_detail", "osd_detail",
		"daemon_log", "slow_ops", "perf_counters", "host_io", "read_log",
		"host_check", "ceph_source_lookup",
	}
	for _, w := range want {
		if _, ok := r.Get(w); !ok {
			t.Errorf("missing tool %q", w)
		}
	}
}

func TestRegistrySpecsHaveSchema(t *testing.T) {
	r := DefaultRegistry(nil)
	for _, spec := range r.Specs() {
		if spec.Name == "" {
			t.Error("empty tool name")
		}
		if spec.Description == "" {
			t.Errorf("%s: empty description", spec.Name)
		}
		if len(spec.Schema) == 0 {
			t.Errorf("%s: empty schema", spec.Name)
		}
		var v map[string]any
		if err := json.Unmarshal(spec.Schema, &v); err != nil {
			t.Errorf("%s: schema is not valid JSON: %v", spec.Name, err)
		}
	}
}

func TestRegistryRejectsDuplicateName(t *testing.T) {
	r := NewRegistry()
	a := &stubTool{name: "x"}
	if err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&stubTool{name: "x"}); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestRegistryDispatch(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubTool{name: "x", out: "hello"})
	res, _, err := r.Execute(context.Background(), "x", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "hello" {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestRegistryExecuteUnknownTool(t *testing.T) {
	r := NewRegistry()
	_, _, err := r.Execute(context.Background(), "nope", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("error = %v", err)
	}
}

type stubTool struct {
	name string
	out  string
}

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Description() string { return "stub" }
func (s *stubTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (*Result, *Raw, error) {
	return &Result{Summary: s.out}, &Raw{}, nil
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/tools/ -v`
Expected: FAIL — `undefined: DefaultRegistry`

- [ ] **Step 3: 实现**

`pkg/doctor/tools/registry.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

// Runner 是工具层对集群的全部依赖。
type Runner interface {
	Run(ctx context.Context, node string, argv []string) (access.Output, error)
	Capabilities(ctx context.Context, node string) access.Capabilities
	Topology(ctx context.Context) (*access.Topology, error)
	Nodes() []string
}

// Result 是工具返回给模型的内容。
type Result struct {
	Summary string   `json:"summary"`          // 一句话结论
	Data    any      `json:"data,omitempty"`   // 结构化细节
	Hints   []string `json:"hints,omitempty"`  // 下一步可执行的查询
}

// Raw 是工具留下的一手材料，由 loop 写入证据链。
type Raw struct {
	Node      string
	Command   string
	Output    string
	Stderr    string
	Duration  time.Duration
	ExitCode  int
	Truncated bool
}

// Tool 是一个诊断工具。
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error)
}

// Spec 是发给模型的工具描述。
type Spec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// Registry 工具注册表。
type Registry struct {
	tools map[string]Tool
	order []string
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) error {
	name := t.Name()
	if name == "" {
		return fmt.Errorf("tool has empty name")
	}
	if _, ok := r.tools[name]; ok {
		return fmt.Errorf("duplicate tool %q", name)
	}
	r.tools[name] = t
	r.order = append(r.order, name)
	return nil
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) Names() []string {
	out := append([]string{}, r.order...)
	sort.Strings(out)
	return out
}

func (r *Registry) Specs() []Spec {
	out := make([]Spec, 0, len(r.order))
	for _, n := range r.Names() {
		t := r.tools[n]
		out = append(out, Spec{Name: t.Name(), Description: t.Description(), Schema: t.Schema()})
	}
	return out
}

// Execute 按名字分发到工具。
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (*Result, *Raw, error) {
	t, ok := r.tools[name]
	if !ok {
		return nil, nil, fmt.Errorf("unknown tool %q", name)
	}
	return t.Execute(ctx, args)
}

// DefaultRegistry 构建全部 12 个工具。
// srcDir 为空时源码反查工具仍注册，但执行时返回 unavailable。
func DefaultRegistry(r Runner) *Registry {
	reg := NewRegistry()
	for _, t := range []Tool{
		NewCephStatus(r),
		NewOSDTree(r),
		NewPoolInfo(r),
		NewPGDetail(r),
		NewOSDDetail(r),
		NewDaemonLog(r),
		NewSlowOps(r),
		NewPerfCounters(r),
		NewHostIO(r),
		NewReadLog(r),
		NewHostCheck(r),
		NewSourceLookup(r, ""),
	} {
		if err := reg.Register(t); err != nil {
			panic(err)
		}
	}
	return reg
}
```

- [ ] **Step 4: 用占位实现让注册表测试可独立通过**

Task 8 只交付注册表骨架。工具构造函数在 Task 9-14 才实现，因此本任务先提供空壳工厂，测试只校验注册表自身行为。

`pkg/doctor/tools/placeholders.go`：

```go
package tools

import "context"

// 以下构造函数在 Task 9-14 中逐个替换为真实实现。
func NewCephStatus(r Runner) Tool          { return &pendingTool{name: "ceph_status"} }
func NewOSDTree(r Runner) Tool             { return &pendingTool{name: "osd_tree"} }
func NewPoolInfo(r Runner) Tool            { return &pendingTool{name: "pool_info"} }
func NewPGDetail(r Runner) Tool            { return &pendingTool{name: "pg_detail"} }
func NewOSDDetail(r Runner) Tool           { return &pendingTool{name: "osd_detail"} }
func NewDaemonLog(r Runner) Tool           { return &pendingTool{name: "daemon_log"} }
func NewSlowOps(r Runner) Tool             { return &pendingTool{name: "slow_ops"} }
func NewPerfCounters(r Runner) Tool        { return &pendingTool{name: "perf_counters"} }
func NewHostIO(r Runner) Tool              { return &pendingTool{name: "host_io"} }
func NewReadLog(r Runner) Tool             { return &pendingTool{name: "read_log"} }
func NewHostCheck(r Runner) Tool           { return &pendingTool{name: "host_check"} }
func NewSourceLookup(r Runner, _ string) Tool { return &pendingTool{name: "ceph_source_lookup"} }

type pendingTool struct{ name string }

func (p *pendingTool) Name() string             { return p.name }
func (p *pendingTool) Description() string      { return "尚未实现" }
func (p *pendingTool) Schema() json.RawMessage  { return json.RawMessage(`{"type":"object","properties":{}}`) }
func (p *pendingTool) Execute(context.Context, json.RawMessage) (*Result, *Raw, error) {
	return nil, nil, fmt.Errorf("%s 尚未实现", p.name)
}
```

导入补上 `"encoding/json"` 与 `"fmt"`。

Run: `go test ./pkg/doctor/tools/ -run TestRegistry -v`
Expected: PASS（12 个工具名与 schema 均已可校验）

Task 9-14 每实现一个工具，就从 `placeholders.go` 删除对应那行。

- [ ] **Step 5: 提交**

```bash
git add pkg/doctor/tools/
git commit -m "feat(doctor/tools): add tool registry, result types and fake runner"
```

---

### Task 9: ceph_status 工具

**Files:**
- Create: `pkg/doctor/tools/ceph_status.go`
- Test: `pkg/doctor/tools/ceph_status_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/tools/ceph_status_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const statusJSON = `{
  "fsid": "abc-123",
  "health": {"status": "HEALTH_WARN", "checks": {"PG_DEGRADED": {"severity": "HEALTH_WARN", "summary": {"message": "Degraded data redundancy"}}}},
  "monmap": {"num_mons": 3},
  "osdmap": {"num_osds": 20, "num_up_osds": 19, "num_in_osds": 19},
  "pgmap": {
    "num_pgs": 256,
    "pgs_by_state": [
      {"state_name": "active+clean", "count": 250},
      {"state_name": "active+undersized+degraded", "count": 6}
    ],
    "bytes_total": 10995116277760,
    "bytes_used": 5497558138880,
    "bytes_avail": 5497558138880
  }
}`

func TestCephStatusParsesHealth(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "status", "-f", "json"}, statusJSON)
	tr.on("node-3", []string{"ceph", "health", "detail"}, "HEALTH_WARN 6 pgs degraded")

	tool := NewCephStatus(tr)
	res, raw, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Summary, "HEALTH_WARN") {
		t.Errorf("summary should mention health: %q", res.Summary)
	}
	d := res.Data.(*CephStatusData)
	if d.Health != "HEALTH_WARN" {
		t.Errorf("health = %q", d.Health)
	}
	if d.NumOSDs != 20 || d.NumOSDsUp != 19 {
		t.Errorf("osd counts = %d/%d", d.NumOSDs, d.NumOSDsUp)
	}
	if d.NumPGs != 256 {
		t.Errorf("pgs = %d", d.NumPGs)
	}
	if d.DegradedPGs != 6 {
		t.Errorf("degraded = %d", d.DegradedPGs)
	}
	if raw.Command == "" {
		t.Error("raw command should be recorded")
	}
}

func TestCephStatusHealthyHasNoHints(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "status", "-f", "json"}, `{
      "health":{"status":"HEALTH_OK"},
      "monmap":{"num_mons":3},
      "osdmap":{"num_osds":20,"num_up_osds":20,"num_in_osds":20},
      "pgmap":{"num_pgs":256,"pgs_by_state":[{"state_name":"active+clean","count":256}],
               "bytes_total":100,"bytes_used":50,"bytes_avail":50}}`)
	tr.on("node-3", []string{"ceph", "health", "detail"}, "HEALTH_OK")

	tool := NewCephStatus(tr)
	res, _, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hints) != 0 {
		t.Errorf("healthy cluster should not hint, got %v", res.Hints)
	}
}

func TestCephStatusHintsOnDegraded(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "status", "-f", "json"}, statusJSON)
	tr.on("node-3", []string{"ceph", "health", "detail"}, "detail")

	tool := NewCephStatus(tr)
	res, _, _ := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if len(res.Hints) == 0 {
		t.Fatal("expected hints for degraded cluster")
	}
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "pg_detail") {
		t.Errorf("expected hint to pg_detail, got %v", res.Hints)
	}
}

func TestCephStatusUnavailableWithoutCLI(t *testing.T) {
	tr := newFakeRunner()
	tr.caps.CephCLI = false
	tr.caps.Backend = "log-only"

	tool := NewCephStatus(tr)
	_, _, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected unavailable error")
	}
	if !strings.Contains(err.Error(), "log-only") && !strings.Contains(err.Error(), "daemon_log") {
		t.Errorf("error should point to log path, got %v", err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/tools/ -run TestCephStatus -v`
Expected: FAIL — `undefined: NewCephStatus`

- [ ] **Step 3: 实现**

`pkg/doctor/tools/ceph_status.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

// CephStatusData 是 ceph_status 的结构化结果。
type CephStatusData struct {
	FSID        string   `json:"fsid"`
	Health      string   `json:"health"`
	HealthChecks []string `json:"health_checks,omitempty"`
	NumMons     int      `json:"num_mons"`
	NumOSDs     int      `json:"num_osds"`
	NumOSDsUp   int      `json:"num_osds_up"`
	NumOSDsIn   int      `json:"num_osds_in"`
	NumPGs      int      `json:"num_pgs"`
	DegradedPGs int      `json:"degraded_pgs"`
	PGState     []PGState `json:"pg_state"`
	BytesTotal  uint64   `json:"bytes_total"`
	BytesUsed   uint64   `json:"bytes_used"`
	UsedPercent float64  `json:"used_percent"`
}

type PGState struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

type cephStatusTool struct{ r Runner }

func NewCephStatus(r Runner) Tool { return &cephStatusTool{r: r} }

func (t *cephStatusTool) Name() string { return "ceph_status" }

func (t *cephStatusTool) Description() string {
	return "获取集群整体状态：健康码、健康检查明细、mon quorum、OSD up/in 数量、PG 状态分布、容量使用率。这是每次诊断的起点。"
}

func (t *cephStatusTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

type rawStatus struct {
	FSID   string `json:"fsid"`
	Health struct {
		Status string                     `json:"status"`
		Checks map[string]json.RawMessage `json:"checks"`
	} `json:"health"`
	Monmap struct {
		NumMons int `json:"num_mons"`
	} `json:"monmap"`
	OSDMap struct {
		NumOSDs   int `json:"num_osds"`
		NumUpOSDs int `json:"num_up_osds"`
		NumInOSDs int `json:"num_in_osds"`
	} `json:"osdmap"`
	PGMap struct {
		NumPGs      int `json:"num_pgs"`
		PGsByState  []struct {
			StateName string `json:"state_name"`
			Count     int    `json:"count"`
		} `json:"pgs_by_state"`
		BytesTotal uint64 `json:"bytes_total"`
		BytesUsed  uint64 `json:"bytes_used"`
		BytesAvail uint64 `json:"bytes_avail"`
	} `json:"pgmap"`
}

func (t *cephStatusTool) Execute(ctx context.Context, _ json.RawMessage) (*Result, *Raw, error) {
	node := primaryNode(t.r)
	caps := t.r.Capabilities(ctx, node)
	if !caps.CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI（后端 %s），无法执行 ceph status；可改用 daemon_log 或 read_log 从日志分析", caps.Backend)
	}

	out, err := t.r.Run(ctx, node, []string{"ceph", "status", "-f", "json"})
	if err != nil {
		return nil, nil, err
	}
	var st rawStatus
	if jerr := json.Unmarshal([]byte(out.Stdout), &st); jerr != nil {
		return nil, nil, fmt.Errorf("解析 ceph status 输出失败: %w", jerr)
	}

	d := &CephStatusData{
		FSID:       st.FSID,
		Health:     st.Health.Status,
		NumMons:    st.Monmap.NumMons,
		NumOSDs:    st.OSDMap.NumOSDs,
		NumOSDsUp:  st.OSDMap.NumUpOSDs,
		NumOSDsIn:  st.OSDMap.NumInOSDs,
		NumPGs:     st.PGMap.NumPGs,
		BytesTotal: st.PGMap.BytesTotal,
		BytesUsed:  st.PGMap.BytesUsed,
	}
	for name := range st.Health.Checks {
		d.HealthChecks = append(d.HealthChecks, name)
	}
	for _, s := range st.PGMap.PGsByState {
		d.PGState = append(d.PGState, PGState{State: s.StateName, Count: s.Count})
		if s.StateName != "active+clean" {
			d.DegradedPGs += s.Count
		}
	}
	if d.BytesTotal > 0 {
		d.UsedPercent = float64(d.BytesUsed) / float64(d.BytesTotal) * 100
	}

	raw := &Raw{Node: node, Command: out.Command, Output: out.Stdout, Duration: out.Duration, ExitCode: out.ExitCode, Truncated: out.Truncated}

	res := &Result{
		Summary: fmt.Sprintf("%s，mon %d，OSD %d 总/%d up/%d in，PG %d（异常 %d），容量 %.1f%%",
			d.Health, d.NumMons, d.NumOSDs, d.NumOSDsUp, d.NumOSDsIn, d.NumPGs, d.DegradedPGs, d.UsedPercent),
		Data: d,
	}
	if d.Health != "HEALTH_OK" {
		res.Hints = append(res.Hints, fmt.Sprintf("集群非健康（%s），可调用 pg_detail 查看卡住的 PG", strings.Join(d.HealthChecks, ",")))
	}
	if d.NumOSDsUp < d.NumOSDs {
		res.Hints = append(res.Hints, fmt.Sprintf("有 %d 个 OSD down，可调用 osd_tree 定位，再用 osd_detail 查看具体 OSD", d.NumOSDs-d.NumOSDsUp))
	}
	if d.UsedPercent >= 75 {
		res.Hints = append(res.Hints, fmt.Sprintf("容量使用率 %.1f%% 偏高，可调用 pool_info 查看是哪个池占满", d.UsedPercent))
	}
	if d.DegradedPGs > 100 {
		res.Hints = append(res.Hints, "异常 PG 数量较多，优先调用 pg_detail 而非逐个查询")
	}
	return res, raw, nil
}

// primaryNode 返回诊断入口节点。
func primaryNode(r Runner) string {
	nodes := r.Nodes()
	if len(nodes) > 0 {
		return nodes[0]
	}
	return ""
}

var _ = access.KindUnavailable
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/doctor/tools/ -run TestCephStatus -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/doctor/tools/ceph_status.go pkg/doctor/tools/ceph_status_test.go
git commit -m "feat(doctor/tools): add ceph_status tool"
```

---

### Task 10: osd_tree 工具

**Files:**
- Create: `pkg/doctor/tools/osd_tree.go`
- Test: `pkg/doctor/tools/osd_tree_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/tools/osd_tree_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const osdDFJSON = `{
  "nodes": [
    {"id": 7, "name": "osd.7", "type": "osd", "status": "up", "device_class": "ssd",
     "kb": 1048576, "kb_used": 950000, "utilization": 90.6, "var": 1.2, "reweight": 1.0, "crush_weight": 1.0, "pgs": 128},
    {"id": 8, "name": "osd.8", "type": "osd", "status": "up", "device_class": "ssd",
     "kb": 1048576, "kb_used": 620000, "utilization": 59.1, "var": 0.6, "reweight": 1.0, "crush_weight": 1.0, "pgs": 128},
    {"id": 9, "name": "osd.9", "type": "osd", "status": "down", "device_class": "hdd",
     "kb": 1048576, "kb_used": 100000, "utilization": 9.5, "var": 0.1, "reweight": 1.0, "crush_weight": 1.0, "pgs": 0}
  ]
}`

func TestOSDTreeParsesAndFlagsHotOSDs(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "osd", "df", "-f", "json"}, osdDFJSON)

	tool := NewOSDTree(tr)
	res, raw, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*OSDTreeData)
	if len(d.OSDs) != 3 {
		t.Fatalf("got %d osds", len(d.OSDs))
	}
	if d.OSDs[0].Utilization != 90.6 {
		t.Errorf("utilization = %v", d.OSDs[0].Utilization)
	}
	// osd.7 使用率最高且明显偏离，应被标为热点
	if len(d.Hotspots) == 0 {
		t.Fatal("expected osd.7 flagged as hotspot")
	}
	if d.Hotspots[0].ID != 7 {
		t.Errorf("hotspot = %d, want 7", d.Hotspots[0].ID)
	}
	if len(d.DownOSDs) != 1 || d.DownOSDs[0].ID != 9 {
		t.Errorf("down osds = %+v", d.DownOSDs)
	}
	if !strings.Contains(res.Summary, "osd.7") {
		t.Errorf("summary should name the hotspot: %q", res.Summary)
	}
	if raw.Command == "" {
		t.Error("raw command missing")
	}
	// 应提示查宿主机盘
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "host_io") {
		t.Errorf("expected host_io hint, got %v", res.Hints)
	}
}

func TestOSDTreeBalancedNoHotspot(t *testing.T) {
	balanced := `{"nodes":[
      {"id":1,"name":"osd.1","type":"osd","status":"up","utilization":60.0,"kb":100,"kb_used":60,"pgs":10},
      {"id":2,"name":"osd.2","type":"osd","status":"up","utilization":61.0,"kb":100,"kb_used":61,"pgs":10}]}`
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "osd", "df", "-f", "json"}, balanced)

	res, _, err := NewOSDTree(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*OSDTreeData)
	if len(d.Hotspots) != 0 {
		t.Errorf("expected no hotspot, got %+v", d.Hotspots)
	}
	for _, h := range res.Hints {
		if strings.Contains(h, "host_io") {
			t.Errorf("no hotspot should not hint host_io: %v", res.Hints)
		}
	}
}

func TestOSDTreeMapsDeviceFromTopology(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "osd", "df", "-f", "json"}, osdDFJSON)

	res, _, err := NewOSDTree(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*OSDTreeData)
	for _, o := range d.OSDs {
		if o.ID == 7 && o.Device != "sdc" {
			t.Errorf("osd.7 device = %q, want sdc (from topology)", o.Device)
		}
		if o.ID == 7 && o.Host != "node-3" {
			t.Errorf("osd.7 host = %q", o.Host)
		}
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/tools/ -run TestOSDTree -v`
Expected: FAIL — `undefined: NewOSDTree`

- [ ] **Step 3: 实现**

`pkg/doctor/tools/osd_tree.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// OSDInfo 单个 OSD 的精简视图。
type OSDInfo struct {
	ID          int     `json:"id"`
	Host        string  `json:"host,omitempty"`
	Device      string  `json:"device,omitempty"`
	Status      string  `json:"status"`
	DeviceClass string  `json:"device_class,omitempty"`
	Utilization float64 `json:"utilization"`
	Var         float64 `json:"var"`
	PGs         int     `json:"pgs"`
}

// OSDTreeData 是 osd_tree 的结构化结果。
type OSDTreeData struct {
	OSDs      []OSDInfo `json:"osds"`
	Hotspots  []OSDInfo `json:"hotspots,omitempty"`
	DownOSDs  []OSDInfo `json:"down_osds,omitempty"`
	MaxUtil   float64   `json:"max_util"`
	AvgUtil   float64   `json:"avg_util"`
}

type osdTreeTool struct{ r Runner }

func NewOSDTree(r Runner) Tool { return &osdTreeTool{r: r} }

func (t *osdTreeTool) Name() string { return "osd_tree" }

func (t *osdTreeTool) Description() string {
	return "获取所有 OSD 的状态、使用率、PG 数量，以及每个 OSD 所在的节点与物理盘。用于发现使用率异常偏高的 OSD 和 down 的 OSD。"
}

func (t *osdTreeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

type rawOSDDF struct {
	Nodes []struct {
		ID          int     `json:"id"`
		Name        string  `json:"name"`
		Type        string  `json:"type"`
		Status      string  `json:"status"`
		DeviceClass string  `json:"device_class"`
		Utilization float64 `json:"utilization"`
		Var         float64 `json:"var"`
		PGs         int     `json:"pgs"`
	} `json:"nodes"`
}

func (t *osdTreeTool) Execute(ctx context.Context, _ json.RawMessage) (*Result, *Raw, error) {
	node := primaryNode(t.r)
	caps := t.r.Capabilities(ctx, node)
	if !caps.CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI（后端 %s），无法执行 ceph osd df", caps.Backend)
	}

	out, err := t.r.Run(ctx, node, []string{"ceph", "osd", "df", "-f", "json"})
	if err != nil {
		return nil, nil, err
	}
	var df rawOSDDF
	if jerr := json.Unmarshal([]byte(out.Stdout), &df); jerr != nil {
		return nil, nil, fmt.Errorf("解析 ceph osd df 输出失败: %w", jerr)
	}

	topo, _ := t.r.Topology(ctx)
	place := map[int]struct{ host, dev string }{}
	if topo != nil {
		for _, p := range topo.OSDs {
			place[p.ID] = struct{ host, dev string }{p.Host, p.Device}
		}
	}

	d := &OSDTreeData{}
	var sumUtil float64
	for _, n := range df.Nodes {
		if n.Type != "osd" {
			continue
		}
		info := OSDInfo{
			ID: n.ID, Status: n.Status, DeviceClass: n.DeviceClass,
			Utilization: n.Utilization, Var: n.Var, PGs: n.PGs,
		}
		if p, ok := place[n.ID]; ok {
			info.Host, info.Device = p.host, p.dev
		}
		d.OSDs = append(d.OSDs, info)
		sumUtil += n.Utilization
		if n.Status != "up" && n.Status != "in" {
			d.DownOSDs = append(d.DownOSDs, info)
		}
	}
	if len(d.OSDs) > 0 {
		d.AvgUtil = sumUtil / float64(len(d.OSDs))
	}

	// 热点：使用率超过均值 15 个百分点，或超过 85%
	for _, o := range d.OSDs {
		if o.Utilization > d.AvgUtil+15 || o.Utilization >= 85 {
			d.Hotspots = append(d.Hotspots, o)
		}
		if o.Utilization > d.MaxUtil {
			d.MaxUtil = o.Utilization
		}
	}
	sort.Slice(d.Hotspots, func(i, j int) bool { return d.Hotspots[i].Utilization > d.Hotspots[j].Utilization })

	raw := &Raw{Node: node, Command: out.Command, Output: out.Stdout, Duration: out.Duration, ExitCode: out.ExitCode, Truncated: out.Truncated}

	res := &Result{
		Data: d,
		Summary: fmt.Sprintf("%d 个 OSD，平均使用率 %.1f%%，最高 %.1f%%（%s）",
			len(d.OSDs), d.AvgUtil, d.MaxUtil, utilLeader(d)),
	}
	if len(d.Hotspots) > 0 {
		var names []string
		for _, h := range d.Hotspots {
			names = append(names, fmt.Sprintf("osd.%d(%.1f%%, %s/%s)", h.ID, h.Utilization, h.Host, h.Device))
		}
		res.Summary += "；异常偏高：" + strings.Join(names, "、")
		res.Hints = append(res.Hints,
			"使用率异常的 OSD 对应盘可能写入变慢，可调用 host_io(nodes, devices) 查看 iostat/dmesg/smartctl",
			"也可调用 osd_detail(osd_id) 查看该 OSD 的运行状态")
	}
	if len(d.DownOSDs) > 0 {
		var ids []string
		for _, o := range d.DownOSDs {
			ids = append(ids, fmt.Sprintf("osd.%d", o.ID))
		}
		res.Hints = append(res.Hints, "存在 down 的 OSD："+strings.Join(ids, "、")+"，调用 osd_detail 查看其状态")
	}
	return res, raw, nil
}

func utilLeader(d *OSDTreeData) string {
	if len(d.OSDs) == 0 {
		return "无"
	}
	best := d.OSDs[0]
	for _, o := range d.OSDs {
		if o.Utilization > best.Utilization {
			best = o
		}
	}
	return fmt.Sprintf("osd.%d", best.ID)
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/doctor/tools/ -run TestOSDTree -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/doctor/tools/osd_tree.go pkg/doctor/tools/osd_tree_test.go
git commit -m "feat(doctor/tools): add osd_tree tool with hotspot detection"
```

---
### Task 11: pool_info 与 pg_detail 工具

**Files:**
- Create: `pkg/doctor/tools/pool_info.go`
- Create: `pkg/doctor/tools/pg_detail.go`
- Test: `pkg/doctor/tools/pool_info_test.go`
- Test: `pkg/doctor/tools/pg_detail_test.go`

- [ ] **Step 1: 写 pool_info 失败测试**

`pkg/doctor/tools/pool_info_test.go`：

```go
package tools

import (
	"context"
	"testing"
)

const poolDFJSON = `{
  "pools": [
    {"name": "rbd", "id": 1, "stats": {"bytes_used": 5497558138880, "max_avail": 5497558138880, "percent_used": 0.5},
     "pg_num": 128, "pg_placement_num": 128, "size": 3, "min_size": 2}
  ]
}`

const poolAutoscaleJSON = `{
  "pools": [
    {"pool_name": "rbd", "pg_num_target": 256, "pg_num": 128, "pg_autoscale_mode": "on"}
  ]
}`

func TestPoolInfoParses(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "df", "detail", "-f", "json"}, poolDFJSON)
	tr.on("node-3", []string{"ceph", "osd", "pool", "autoscale-status", "-f", "json"}, poolAutoscaleJSON)

	res, raw, err := NewPoolInfo(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*PoolInfoData)
	if len(d.Pools) != 1 {
		t.Fatalf("got %d pools", len(d.Pools))
	}
	if d.Pools[0].Name != "rbd" || d.Pools[0].PGNum != 128 {
		t.Errorf("unexpected pool: %+v", d.Pools[0])
	}
	// PG 数低于目标一半，应提示
	if len(res.Hints) == 0 {
		t.Error("expected hint about pg imbalance")
	}
	if raw.Command == "" {
		t.Error("raw missing")
	}
}

func TestPoolInfoDegradedPGs(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "df", "detail", "-f", "json"}, `{"pools":[
      {"name":"x","stats":{"bytes_used":1,"max_avail":1,"percent_used":0.9},
       "pg_num":64,"size":3,"min_size":2}]}`)
	tr.on("node-3", []string{"ceph", "osd", "pool", "autoscale-status", "-f", "json"}, `{"pools":[]}`)

	res, _, err := NewPoolInfo(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*PoolInfoData)
	if d.Pools[0].UsedPercent < 0.8 {
		t.Errorf("used percent = %v", d.Pools[0].UsedPercent)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/tools/ -run TestPoolInfo -v`
Expected: FAIL — `undefined: NewPoolInfo`

- [ ] **Step 3: 实现 pool_info**

`pkg/doctor/tools/pool_info.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// PoolInfo 单个池的容量信息。
type PoolInfo struct {
	Name          string  `json:"name"`
	BytesUsed     uint64  `json:"bytes_used"`
	MaxAvail      uint64  `json:"max_avail"`
	UsedPercent   float64 `json:"used_percent"` // 0-1
	PGNum         int     `json:"pg_num"`
	PGNumTarget   int     `json:"pg_num_target,omitempty"`
	AutoscaleMode string  `json:"autoscale_mode,omitempty"`
	Size          int     `json:"size"`
	MinSize       int     `json:"min_size"`
}

// PoolInfoData 是 pool_info 的结构化结果。
type PoolInfoData struct {
	Pools []PoolInfo `json:"pools"`
}

type poolInfoTool struct{ r Runner }

func NewPoolInfo(r Runner) Tool { return &poolInfoTool{r: r} }

func (t *poolInfoTool) Name() string { return "pool_info" }

func (t *poolInfoTool) Description() string {
	return "获取各存储池的容量使用、副本数、PG 数量及 autoscale 目标值。用于定位是哪个池接近写满，以及 PG 数量是否严重偏离目标。"
}

func (t *poolInfoTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

type rawPoolDF struct {
	Pools []struct {
		Name  string `json:"name"`
		Stats struct {
			BytesUsed   uint64  `json:"bytes_used"`
			MaxAvail    uint64  `json:"max_avail"`
			PercentUsed float64 `json:"percent_used"`
		} `json:"stats"`
		PGNum    int `json:"pg_num"`
		Size     int `json:"size"`
		MinSize  int `json:"min_size"`
	} `json:"pools"`
}

type rawAutoscale struct {
	Pools []struct {
		PoolName        string `json:"pool_name"`
		PGNumTarget     int    `json:"pg_num_target"`
		PGNum           int    `json:"pg_num"`
		PGAutoscaleMode string `json:"pg_autoscale_mode"`
	} `json:"pools"`
}

func (t *poolInfoTool) Execute(ctx context.Context, _ json.RawMessage) (*Result, *Raw, error) {
	node := primaryNode(t.r)
	if !t.r.Capabilities(ctx, node).CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI，无法执行 ceph df detail")
	}

	out, err := t.r.Run(ctx, node, []string{"ceph", "df", "detail", "-f", "json"})
	if err != nil {
		return nil, nil, err
	}
	var df rawPoolDF
	if err := json.Unmarshal([]byte(out.Stdout), &df); err != nil {
		return nil, nil, fmt.Errorf("解析 ceph df detail 失败: %w", err)
	}

	autoMap := map[string]struct {
		target int
		mode   string
	}{}
	if aout, aerr := t.r.Run(ctx, node, []string{"ceph", "osd", "pool", "autoscale-status", "-f", "json"}); aerr == nil {
		var au rawAutoscale
		if json.Unmarshal([]byte(aout.Stdout), &au) == nil {
			for _, p := range au.Pools {
				autoMap[p.PoolName] = struct {
					target int
					mode   string
				}{p.PGNumTarget, p.PGAutoscaleMode}
			}
		}
	}

	d := &PoolInfoData{}
	for _, p := range df.Pools {
		pi := PoolInfo{
			Name: p.Name, BytesUsed: p.Stats.BytesUsed, MaxAvail: p.Stats.MaxAvail,
			UsedPercent: p.Stats.PercentUsed, PGNum: p.PGNum, Size: p.Size, MinSize: p.MinSize,
		}
		if a, ok := autoMap[p.Name]; ok {
			pi.PGNumTarget, pi.AutoscaleMode = a.target, a.mode
		}
		d.Pools = append(d.Pools, pi)
	}

	raw := &Raw{Node: node, Command: out.Command, Output: out.Stdout, Duration: out.Duration, ExitCode: out.ExitCode, Truncated: out.Truncated}

	res := &Result{Data: d, Summary: fmt.Sprintf("%d 个池", len(d.Pools))}
	var hot []string
	for _, p := range d.Pools {
		if p.UsedPercent >= 0.8 {
			hot = append(hot, fmt.Sprintf("%s(%.0f%%)", p.Name, p.UsedPercent*100))
		}
		if p.PGNumTarget > 0 && p.PGNumTarget >= p.PGNum*2 {
			res.Hints = append(res.Hints, fmt.Sprintf("池 %s 的 PG 数 %d 远低于目标 %d，可以建议调整 pg_num（需人工确认）", p.Name, p.PGNum, p.PGNumTarget))
		}
	}
	if len(hot) > 0 {
		res.Summary += "；接近写满：" + strings.Join(hot, "、")
		res.Hints = append(res.Hints, "存在接近写满的池，可调用 osd_tree 确认是否个别 OSD 先满")
	}
	return res, raw, nil
}
```

- [ ] **Step 4: 写 pg_detail 失败测试**

`pkg/doctor/tools/pg_detail_test.go`：

```go
package tools

import (
	"context"
	"strings"
	"testing"
)

func TestPGDetailParsesStuck(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "pg", "stat"}, "256 pgs: 250 active+clean; 6 active+undersized+degraded")
	tr.on("node-3", []string{"ceph", "pg", "dump_stuck"}, "2.4a active+undersized+degraded 2026-10-23T15:00:00 3600s 8m\n2.7f active+undersized 2026-10-23T15:00:00 3600s 8m\n")

	res, raw, err := NewPGDetail(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*PGDetailData)
	if d.StuckCount != 2 {
		t.Errorf("stuck = %d", d.StuckCount)
	}
	if len(d.StuckPGs) != 2 || d.StuckPGs[0].ID != "2.4a" {
		t.Errorf("stuck pgs = %+v", d.StuckPGs)
	}
	if !strings.Contains(res.Summary, "2") {
		t.Errorf("summary = %q", res.Summary)
	}
	if raw.Command == "" {
		t.Error("raw missing")
	}
	// 卡住 PG 应提示查对应 OSD
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "osd_detail") {
		t.Errorf("expected osd_detail hint, got %v", res.Hints)
	}
}

func TestPGDetailNoStuck(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "pg", "stat"}, "256 pgs: 256 active+clean")
	tr.on("node-3", []string{"ceph", "pg", "dump_stuck"}, "")

	res, _, err := NewPGDetail(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*PGDetailData)
	if d.StuckCount != 0 {
		t.Errorf("stuck = %d", d.StuckCount)
	}
	if len(res.Hints) != 0 {
		t.Errorf("no stuck should not hint, got %v", res.Hints)
	}
}
```

- [ ] **Step 5: 运行测试确认失败**

Run: `go test ./pkg/doctor/tools/ -run TestPGDetail -v`
Expected: FAIL — `undefined: NewPGDetail`

- [ ] **Step 6: 实现 pg_detail**

`pkg/doctor/tools/pg_detail.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

// StuckPG 一个卡住的 PG。
type StuckPG struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// PGDetailData 是 pg_detail 的结构化结果。
type PGDetailData struct {
	Stat       string    `json:"stat"`
	StuckPGs   []StuckPG `json:"stuck_pgs,omitempty"`
	StuckCount int       `json:"stuck_count"`
}

type pgDetailTool struct{ r Runner }

func NewPGDetail(r Runner) Tool { return &pgDetailTool{r: r} }

func (t *pgDetailTool) Name() string { return "pg_detail" }

func (t *pgDetailTool) Description() string {
	return "查看 PG 汇总状态与卡住（stuck）的 PG 列表。用于分析 degraded / peering / stale 等异常 PG 的规模与具体编号。"
}

func (t *pgDetailTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"state":{"type":"string","description":"可选，只看特定状态，如 peering / degraded / stale"}},"additionalProperties":false}`)
}

func (t *pgDetailTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	node := primaryNode(t.r)
	if !t.r.Capabilities(ctx, node).CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI，无法执行 ceph pg")
	}
	var in struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(args, &in)

	statOut, err := t.r.Run(ctx, node, []string{"ceph", "pg", "stat"})
	if err != nil {
		return nil, nil, err
	}
	dumpArgs := []string{"ceph", "pg", "dump_stuck"}
	if in.State != "" {
		dumpArgs = append(dumpArgs, in.State)
	}
	dumpOut, derr := t.r.Run(ctx, node, dumpArgs)
	if derr != nil {
		// dump_stuck 失败不阻塞，stat 仍有用
		dumpOut = access.Output{}
	}

	d := &PGDetailData{Stat: strings.TrimSpace(statOut.Stdout)}
	for _, line := range strings.Split(dumpOut.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if !looksLikePGID(fields[0]) {
			continue
		}
		d.StuckPGs = append(d.StuckPGs, StuckPG{ID: fields[0], State: fields[1]})
	}
	d.StuckCount = len(d.StuckPGs)

	raw := &Raw{Node: node, Command: dumpOut.Command, Output: dumpOut.Stdout, Duration: dumpOut.Duration, ExitCode: dumpOut.ExitCode, Truncated: dumpOut.Truncated}

	res := &Result{
		Data:    d,
		Summary: fmt.Sprintf("PG 汇总：%s；卡住 %d 个", d.Stat, d.StuckCount),
	}
	if d.StuckCount > 0 {
		var ids []string
		for i, p := range d.StuckPGs {
			if i >= 5 {
				ids = append(ids, "...")
				break
			}
			ids = append(ids, p.ID)
		}
		res.Hints = append(res.Hints,
			"卡住的 PG："+strings.Join(ids, "、")+". 调用 osd_detail(osd_id) 查看其主 OSD 状态，或 daemon_log 查看该 OSD 的恢复日志")
	}
	return res, raw, nil
}

// looksLikePGID 判断字符串是否形如 2.4a。
func looksLikePGID(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
	}
	return true
}
```

- [ ] **Step 7: 运行测试确认通过**

Run: `go test ./pkg/doctor/tools/ -run 'TestPoolInfo|TestPGDetail' -v`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add pkg/doctor/tools/pool_info.go pkg/doctor/tools/pool_info_test.go pkg/doctor/tools/pg_detail.go pkg/doctor/tools/pg_detail_test.go
git commit -m "feat(doctor/tools): add pool_info and pg_detail tools"
```

---

### Task 12: osd_detail 与 daemon_log 工具

**Files:**
- Create: `pkg/doctor/tools/osd_detail.go`
- Create: `pkg/doctor/tools/daemon_log.go`
- Test: `pkg/doctor/tools/osd_detail_test.go`
- Test: `pkg/doctor/tools/daemon_log_test.go`

- [ ] **Step 1: 写 osd_detail 失败测试**

`pkg/doctor/tools/osd_detail_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestOSDDetailReturnsStatusAndPlacement(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "tell", "osd.7", "status"}, `{"osd":{"osd":7,"up_since":"2026-10-23T10:00:00","bootstrap_osd":false},"state":"ACTIVE"}`)

	res, raw, err := NewOSDDetail(tr).Execute(context.Background(), json.RawMessage(`{"osd_id":7}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*OSDDetailData)
	if d.ID != 7 {
		t.Errorf("id = %d", d.ID)
	}
	if d.Host != "node-3" {
		t.Errorf("host = %q, want node-3 from topology", d.Host)
	}
	if d.Device != "sdc" {
		t.Errorf("device = %q, want sdc", d.Device)
	}
	if !strings.Contains(res.Summary, "osd.7") {
		t.Errorf("summary = %q", res.Summary)
	}
	if raw.Command == "" {
		t.Error("raw missing")
	}
	// 有设备时应提示 host_io
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "host_io") {
		t.Errorf("expected host_io hint, got %v", res.Hints)
	}
}

func TestOSDDetailRequiresID(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewOSDDetail(tr).Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing osd_id")
	}
}

func TestOSDDetailUnknownOSD(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewOSDDetail(tr).Execute(context.Background(), json.RawMessage(`{"osd_id":99}`))
	if err != nil {
		t.Fatal(err) // 拓扑里没有该 OSD 时仍应返回结果，只是缺 host/device
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/tools/ -run TestOSDDetail -v`
Expected: FAIL — `undefined: NewOSDDetail`

- [ ] **Step 3: 实现 osd_detail**

`pkg/doctor/tools/osd_detail.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// OSDDetailData 是 osd_detail 的结构化结果。
type OSDDetailData struct {
	ID        int    `json:"id"`
	Host      string `json:"host,omitempty"`
	Device    string `json:"device,omitempty"`
	RawDevice string `json:"raw_device,omitempty"`
	Status    string `json:"status_raw,omitempty"`
}

type osdDetailTool struct{ r Runner }

func NewOSDDetail(r Runner) Tool { return &osdDetailTool{r: r} }

func (t *osdDetailTool) Name() string { return "osd_detail" }

func (t *osdDetailTool) Description() string {
	return "查看单个 OSD 的运行状态，并给出它所在节点与物理盘路径。用于把 OSD 问题关联到具体宿主机和磁盘。"
}

func (t *osdDetailTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"osd_id":{"type":"integer","description":"OSD 编号，例如 7"}},"required":["osd_id"],"additionalProperties":false}`)
}

func (t *osdDetailTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		OSDID *int `json:"osd_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	if in.OSDID == nil {
		return nil, nil, fmt.Errorf("缺少必填参数 osd_id")
	}
	id := *in.OSDID

	node := primaryNode(t.r)
	if !t.r.Capabilities(ctx, node).CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI，无法执行 ceph tell")
	}

	d := &OSDDetailData{ID: id}
	if topo, err := t.r.Topology(ctx); err == nil && topo != nil {
		for _, p := range topo.OSDs {
			if p.ID == id {
				d.Host, d.Device, d.RawDevice = p.Host, p.Device, p.RawDevice
				break
			}
		}
	}

	out, err := t.r.Run(ctx, node, []string{"ceph", "tell", fmt.Sprintf("osd.%d", id), "status"})
	if err != nil {
		// 拿不到 status 时仍返回拓扑信息
		res := &Result{
			Data:    d,
			Summary: fmt.Sprintf("osd.%d 位于 %s，盘 %s（实时状态获取失败）", id, orUnknown(d.Host), orUnknown(d.Device)),
			Hints:   osdHints(d),
		}
		return res, &Raw{Node: node, Command: out.Command}, nil
	}

	d.Status = out.Stdout
	raw := &Raw{Node: node, Command: out.Command, Output: out.Stdout, Duration: out.Duration, ExitCode: out.ExitCode, Truncated: out.Truncated}

	res := &Result{
		Data:    d,
		Summary: fmt.Sprintf("osd.%d 位于节点 %s，物理盘 %s", id, orUnknown(d.Host), orUnknown(d.Device)),
		Hints:   osdHints(d),
	}
	return res, raw, nil
}

func osdHints(d *OSDDetailData) []string {
	if d.Host == "" || d.Device == "" {
		return []string{"缺少节点/磁盘映射，可调用 osd_tree 获取，或 daemon_log 查看该 OSD 日志"}
	}
	return []string{
		fmt.Sprintf("调用 host_io(node=%q, device=%q) 查看该盘的 iostat / dmesg / smartctl", d.Host, d.Device),
		fmt.Sprintf("调用 daemon_log(node=%q, daemon=osd, id=%d) 查看该 OSD 日志", d.Host, d.ID),
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "未知"
	}
	return s
}
```

- [ ] **Step 4: 写 daemon_log 失败测试**

`pkg/doctor/tools/daemon_log_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

func TestDaemonLogFallsBackToRookLogDir(t *testing.T) {
	tr := newFakeRunner()
	// /var/log/ceph 路径不存在，rooke 日志存在
	tr.fail("node-3", []string{"ls", "-d", "/var/log/ceph"}, &access.ToolError{Kind: access.KindFailed, Message: "no such file"})
	tr.on("node-3", []string{"ls", "-d", "/var/lib/rook/rook-ceph/logs"}, "/var/lib/rook/rook-ceph/logs\n")
	tr.on("node-3", []string{"tail", "-n", "300", "/var/lib/rook/rook-ceph/logs/rook-ceph-osd.7.log"}, "2026-10-23 15:00:00.000 osd.7 heartbeat_check: no reply\n")

	res, raw, err := NewDaemonLog(tr).Execute(context.Background(), json.RawMessage(`{"daemon":"osd","id":7,"lines":300}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*DaemonLogData)
	if !strings.Contains(d.Path, "/var/lib/rook/rook-ceph/logs") {
		t.Errorf("path = %q", d.Path)
	}
	if !strings.Contains(res.Summary, "宿主机日志") {
		t.Errorf("summary should name the source: %q", res.Summary)
	}
	if raw.Output == "" {
		t.Error("raw output missing")
	}
}

func TestDaemonLogRequiresDaemon(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewDaemonLog(tr).Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing daemon")
	}
}

func TestDaemonLogRejectsUnknownDaemon(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewDaemonLog(tr).Execute(context.Background(), json.RawMessage(`{"daemon":"evil"}`))
	if err == nil {
		t.Fatal("expected error for unknown daemon type")
	}
}
```

- [ ] **Step 5: 运行测试确认失败**

Run: `go test ./pkg/doctor/tools/ -run TestDaemonLog -v`
Expected: FAIL — `undefined: NewDaemonLog`

- [ ] **Step 6: 实现 daemon_log**

`pkg/doctor/tools/daemon_log.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

// DaemonLogData 是 daemon_log 的结构化结果。
type DaemonLogData struct {
	Path   string `json:"path"`
	Source string `json:"source"` // host-log / rook-log / kubectl-logs / crictl-logs
	Lines  int    `json:"lines"`
}

type daemonLogTool struct{ r Runner }

func NewDaemonLog(r Runner) Tool { return &daemonLogTool{r: r} }

func (t *daemonLogTool) Name() string { return "daemon_log" }

func (t *daemonLogTool) Description() string {
	return "读取指定 Ceph 组件（mon/mgr/osd/mds/rgw）的日志尾部。自动在宿主机日志目录与容器日志之间选择可用的来源。"
}

func (t *daemonLogTool) Schema() json.RawMessage {
	return json.RawMessage(`{
      "type":"object",
      "properties":{
        "daemon":{"type":"string","enum":["mon","mgr","osd","mds","rgw"],"description":"组件类型"},
        "id":{"type":"string","description":"组件编号，如 7 或 a；mgr 可省略"},
        "node":{"type":"string","description":"节点名，默认入口节点"},
        "lines":{"type":"integer","description":"读取行数，默认 300，最大 5000"}
      },
      "required":["daemon"],
      "additionalProperties":false
    }`)
}

func (t *daemonLogTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		Daemon string `json:"daemon"`
		ID     string `json:"id"`
		Node   string `json:"node"`
		Lines  int    `json:"lines"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	switch in.Daemon {
	case "mon", "mgr", "osd", "mds", "rgw":
	default:
		return nil, nil, fmt.Errorf("不支持的 daemon 类型 %q，可选 mon/mgr/osd/mds/rgw", in.Daemon)
	}
	if in.Node == "" {
		in.Node = primaryNode(t.r)
	}
	if in.Lines <= 0 {
		in.Lines = 300
	}
	if in.Lines > 5000 {
		in.Lines = 5000
	}

	caps := t.r.Capabilities(ctx, in.Node)
	fileName := daemonFileName(in.Daemon, in.ID)

	// 1. 宿主机标准日志目录
	if hasDir(caps, "/var/log/ceph") {
		p := path.Join("/var/log/ceph", fileName)
		if out, err := t.r.Run(ctx, in.Node, []string{"tail", "-n", fmt.Sprint(in.Lines), p}); err == nil {
			return daemonResult(in.Daemon, p, "host-log", in.Lines), daemonRaw(in.Node, out), nil
		}
	}
	// 2. Rook 日志目录
	if hasDir(caps, "/var/lib/rook/rook-ceph/logs") {
		p := path.Join("/var/lib/rook/rook-ceph/logs", fileName)
		if out, err := t.r.Run(ctx, in.Node, []string{"tail", "-n", fmt.Sprint(in.Lines), p}); err == nil {
			return daemonResult(in.Daemon, p, "rook-log", in.Lines), daemonRaw(in.Node, out), nil
		}
	}
	// 3. kubectl logs
	if caps.Kubectl {
		pod := fmt.Sprintf("rook-ceph-%s-%s", in.Daemon, in.ID)
		if in.Daemon == "mgr" || in.Daemon == "mon" {
			// 名称含随机后缀，用 -l 选择器更稳；此处给出尝试命令
			pod = fmt.Sprintf("rook-ceph-%s", in.Daemon)
		}
		out, err := t.r.Run(ctx, in.Node, []string{"kubectl", "-n", "rook-ceph", "logs", pod, "--tail", fmt.Sprint(in.Lines)})
		if err == nil {
			return daemonResult(in.Daemon, "kubectl logs "+pod, "kubectl-logs", in.Lines), daemonRaw(in.Node, out), nil
		}
	}

	return nil, nil, fmt.Errorf("在节点 %s 上找不到 %s 的日志（后端 %s，日志目录 %v）", in.Node, fileName, caps.Backend, caps.LogDirs)
}

func daemonResult(daemon, p, source string, lines int) *Result {
	return &Result{
		Data:    &DaemonLogData{Path: p, Source: source, Lines: lines},
		Summary: fmt.Sprintf("已读取 %s 的日志（来源：%s，路径 %s，%d 行）", daemon, source, p, lines),
	}
}

func daemonRaw(node string, out access.Output) *Raw {
	return &Raw{Node: node, Command: out.Command, Output: out.Stdout, Stderr: out.Stderr, Duration: out.Duration, ExitCode: out.ExitCode, Truncated: out.Truncated}
}

func daemonFileName(daemon, id string) string {
	if id == "" {
		return "ceph-" + daemon + ".log"
	}
	return fmt.Sprintf("ceph-%s.%s.log", daemon, id)
}

func hasDir(caps access.Capabilities, dir string) bool {
	for _, d := range caps.LogDirs {
		if d == dir {
			return true
		}
	}
	return false
}

var _ = strings.TrimSpace
```

- [ ] **Step 7: 运行测试确认通过**

Run: `go test ./pkg/doctor/tools/ -run 'TestOSDDetail|TestDaemonLog' -v`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add pkg/doctor/tools/osd_detail.go pkg/doctor/tools/osd_detail_test.go pkg/doctor/tools/daemon_log.go pkg/doctor/tools/daemon_log_test.go
git commit -m "feat(doctor/tools): add osd_detail and daemon_log tools"
```

---
### Task 13: slow_ops、perf_counters 与 host_io 工具

**Files:**
- Create: `pkg/doctor/tools/slow_ops.go`
- Create: `pkg/doctor/tools/perf_counters.go`
- Create: `pkg/doctor/tools/host_io.go`
- Test: `pkg/doctor/tools/slow_ops_test.go`
- Test: `pkg/doctor/tools/perf_counters_test.go`
- Test: `pkg/doctor/tools/host_io_test.go`

- [ ] **Step 1: 写 slow_ops 失败测试**

`pkg/doctor/tools/slow_ops_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const historicOpsJSON = `{
  "num_ops": 2,
  "ops": [
    {"description":"osd_op(client.4123.0:1 2.4a 2:abc [write 0~4096] v1)","initiated_at":{"sec":1750000000},"age":32.5,
     "events":[{"event":"queued_for_pg","time":"2026-10-23T15:00:00"},{"event":"waiting_for_ondisk","time":"2026-10-23T15:00:28"}]},
    {"description":"osd_op(client.4123.0:2 2.7f 2:def [write 0~4096] v1)","initiated_at":{"sec":1750000001},"age":28.1,
     "events":[{"event":"queued_for_pg","time":"2026-10-23T15:00:01"},{"event":"waiting_for_ondisk","time":"2026-10-23T15:00:25"}]}
  ]
}`

func TestSlowOpsParsesHistoricOps(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "tell", "osd.7", "dump_historic_ops"}, historicOpsJSON)
	tr.on("node-3", []string{"ceph", "tell", "osd.8", "dump_historic_ops"}, `{"num_ops":0,"ops":[]}`)
	tr.on("node-3", []string{"ceph", "tell", "osd.9", "dump_historic_ops"}, `{"num_ops":0,"ops":[]}`)

	res, raw, err := NewSlowOps(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*SlowOpsData)
	if d.TotalOps != 2 {
		t.Errorf("total = %d", d.TotalOps)
	}
	if d.PerOSD[7] != 2 {
		t.Errorf("osd.7 slow ops = %d", d.PerOSD[7])
	}
	if len(d.TopOps) != 2 {
		t.Errorf("top ops = %d", len(d.TopOps))
	}
	if d.TopOps[0].Duration < 32 || d.TopOps[0].Duration > 33 {
		t.Errorf("duration = %v", d.TopOps[0].Duration)
	}
	// 卡在 waiting_for_ondisk 应给出磁盘方向提示
	if !strings.Contains(strings.Join(d.TopOps[0].Events, ","), "waiting_for_ondisk") {
		t.Errorf("events = %v", d.TopOps[0].Events)
	}
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "host_io") {
		t.Errorf("waiting_for_ondisk should hint host_io, got %v", res.Hints)
	}
	if raw.Command == "" {
		t.Error("raw missing")
	}
}

func TestSlowOpsNone(t *testing.T) {
	tr := newFakeRunner()
	for _, id := range []string{"7", "8", "9"} {
		tr.on("node-3", []string{"ceph", "tell", "osd." + id, "dump_historic_ops"}, `{"num_ops":0,"ops":[]}`)
	}
	res, _, err := NewSlowOps(tr).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*SlowOpsData)
	if d.TotalOps != 0 {
		t.Errorf("total = %d", d.TotalOps)
	}
	if len(res.Hints) != 0 {
		t.Errorf("no slow ops should not hint, got %v", res.Hints)
	}
}

func TestSlowOpsAcceptsExplicitOSDs(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "tell", "osd.9", "dump_historic_ops"}, `{"num_ops":0,"ops":[]}`)

	_, _, err := NewSlowOps(tr).Execute(context.Background(), json.RawMessage(`{"osd_ids":[9]}`))
	if err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/tools/ -run TestSlowOps -v`
Expected: FAIL — `undefined: NewSlowOps`

- [ ] **Step 3: 实现 slow_ops**

`pkg/doctor/tools/slow_ops.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SlowOp 单条慢操作。
type SlowOp struct {
	OSD         int      `json:"osd"`
	Description string   `json:"description"`
	Duration    float64  `json:"duration_sec"`
	Events      []string `json:"events,omitempty"`
}

// SlowOpsData 是 slow_ops 的结构化结果。
type SlowOpsData struct {
	TotalOps int            `json:"total_ops"`
	PerOSD   map[int]int    `json:"per_osd"`
	TopOps   []SlowOp       `json:"top_ops"`
}

type slowOpsTool struct{ r Runner }

func NewSlowOps(r Runner) Tool { return &slowOpsTool{r: r} }

func (t *slowOpsTool) Name() string { return "slow_ops" }

func (t *slowOpsTool) Description() string {
	return "采集所有 OSD 的 slow/historic ops，给出耗时最长的操作及其内部阶段时间线（如 waiting_for_ondisk 表示卡在磁盘 fsync）。用于性能问题定位。"
}

func (t *slowOpsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"osd_ids":{"type":"array","items":{"type":"integer"},"description":"可选，只查这些 OSD；默认查全部"}},"additionalProperties":false}`)
}

type rawHistoricOps struct {
	NumOps int `json:"num_ops"`
	Ops    []struct {
		Description string  `json:"description"`
		Age         float64 `json:"age"`
		Events      []struct {
			Event string `json:"event"`
			Time  string `json:"time"`
		} `json:"events"`
	} `json:"ops"`
}

func (t *slowOpsTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		OSDIDs []int `json:"osd_ids"`
	}
	_ = json.Unmarshal(args, &in)

	node := primaryNode(t.r)
	if !t.r.Capabilities(ctx, node).CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI，无法执行 ceph tell dump_historic_ops")
	}

	ids := in.OSDIDs
	if len(ids) == 0 {
		topo, err := t.r.Topology(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, o := range topo.OSDs {
			ids = append(ids, o.ID)
		}
	}
	if len(ids) > 64 {
		ids = ids[:64] // 大集群下限制单次采集量
	}

	d := &SlowOpsData{PerOSD: map[int]int{}}
	var lastRaw *Raw
	var firstErr error
	queried := 0
	for _, id := range ids {
		out, err := t.r.Run(ctx, node, []string{"ceph", "tell", fmt.Sprintf("osd.%d", id), "dump_historic_ops"})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		queried++
		lastRaw = &Raw{Node: node, Command: out.Command, Output: out.Stdout, Duration: out.Duration, ExitCode: out.ExitCode, Truncated: out.Truncated}

		var h rawHistoricOps
		if json.Unmarshal([]byte(out.Stdout), &h) != nil {
			continue
		}
		d.PerOSD[id] = h.NumOps
		d.TotalOps += h.NumOps
		for _, op := range h.Ops {
			so := SlowOp{OSD: id, Description: op.Description, Duration: op.Age}
			for _, e := range op.Events {
				so.Events = append(so.Events, e.Event)
			}
			d.TopOps = append(d.TopOps, so)
		}
	}
	if queried == 0 && firstErr != nil {
		return nil, nil, firstErr
	}

	sort.Slice(d.TopOps, func(i, j int) bool { return d.TopOps[i].Duration > d.TopOps[j].Duration })
	if len(d.TopOps) > 10 {
		d.TopOps = d.TopOps[:10]
	}

	res := &Result{
		Data:    d,
		Summary: fmt.Sprintf("检查 %d 个 OSD，共 %d 个 slow op", queried, d.TotalOps),
	}
	if d.TotalOps > 0 {
		var hot []string
		for id, n := range d.PerOSD {
			if n > 0 {
				hot = append(hot, fmt.Sprintf("osd.%d(%d)", id, n))
			}
		}
		sort.Strings(hot)
		res.Summary += "；分布：" + strings.Join(hot, "、")

		top := d.TopOps[0]
		res.Summary += fmt.Sprintf("；最长 %.1fs", top.Duration)

		if containsEvent(top.Events, "waiting_for_ondisk") {
			res.Hints = append(res.Hints,
				fmt.Sprintf("最慢的 op 卡在 waiting_for_ondisk（等待磁盘 fsync），调用 osd_detail(osd_id=%d) 获取节点与磁盘，再调用 host_io 检查该盘", top.OSD))
		} else {
			res.Hints = append(res.Hints, fmt.Sprintf("调用 osd_detail(osd_id=%d) 查看该 OSD 所在节点与磁盘", top.OSD))
		}
		res.Hints = append(res.Hints, "若多个 OSD 同时变慢，可调用 host_check 检查对应节点的整体负载")
	}
	return res, lastRaw, nil
}

func containsEvent(events []string, want string) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: 写 perf_counters 失败测试**

`pkg/doctor/tools/perf_counters_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPerfCountersParses(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"ceph", "tell", "osd.7", "perf", "dump"}, `{
      "osd":{
        "op_w_latency":{"avgcount":100,"sum":2.5,"avg":0.025},
        "op_r_latency":{"avgcount":100,"sum":1.5,"avg":0.015},
        "commit_latency_ms":{"avgcount":100,"sum":300,"avg":3.0}
      }}`)

	res, raw, err := NewPerfCounters(tr).Execute(context.Background(), json.RawMessage(`{"osd_id":7}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*PerfCountersData)
	if d.OSD != 7 {
		t.Errorf("osd = %d", d.OSD)
	}
	if got := d.Avg["op_w_latency"]; got != 0.025 {
		t.Errorf("op_w_latency = %v", got)
	}
	if !strings.Contains(res.Summary, "osd.7") {
		t.Errorf("summary = %q", res.Summary)
	}
	if raw.Command == "" {
		t.Error("raw missing")
	}
}

func TestPerfCountersRequiresOSD(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewPerfCounters(tr).Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing osd_id")
	}
}
```

- [ ] **Step 5: 运行测试确认失败**

Run: `go test ./pkg/doctor/tools/ -run TestPerfCounters -v`
Expected: FAIL — `undefined: NewPerfCounters`

- [ ] **Step 6: 实现 perf_counters**

`pkg/doctor/tools/perf_counters.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// PerfCountersData 是 perf_counters 的结构化结果。
type PerfCountersData struct {
	OSD int                `json:"osd"`
	Avg map[string]float64 `json:"avg"`
}

type perfCountersTool struct{ r Runner }

func NewPerfCounters(r Runner) Tool { return &perfCountersTool{r: r} }

func (t *perfCountersTool) Name() string { return "perf_counters" }

func (t *perfCountersTool) Description() string {
	return "采集单个 OSD 的 perf counters（op 读写延迟、commit/apply 延迟、缓存命中等内部计数器）。用于在 slow ops 之外做更细的性能判断。"
}

func (t *perfCountersTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"osd_id":{"type":"integer","description":"OSD 编号"}},"required":["osd_id"],"additionalProperties":false}`)
}

type rawPerfDump map[string]map[string]struct {
	AvgCount float64 `json:"avgcount"`
	Sum      float64 `json:"sum"`
	Avg      float64 `json:"avg"`
}

func (t *perfCountersTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		OSDID *int `json:"osd_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	if in.OSDID == nil {
		return nil, nil, fmt.Errorf("缺少必填参数 osd_id")
	}
	id := *in.OSDID

	node := primaryNode(t.r)
	if !t.r.Capabilities(ctx, node).CephCLI {
		return nil, nil, fmt.Errorf("此集群没有可用的 ceph CLI，无法执行 perf dump")
	}

	out, err := t.r.Run(ctx, node, []string{"ceph", "tell", fmt.Sprintf("osd.%d", id), "perf", "dump"})
	if err != nil {
		return nil, nil, err
	}
	var dump rawPerfDump
	if jerr := json.Unmarshal([]byte(out.Stdout), &dump); jerr != nil {
		return nil, nil, fmt.Errorf("解析 perf dump 失败: %w", jerr)
	}

	d := &PerfCountersData{OSD: id, Avg: map[string]float64{}}
	var keys []string
	for _, section := range dump {
		for name, c := range section {
			d.Avg[name] = c.Avg
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)

	raw := &Raw{Node: node, Command: out.Command, Output: out.Stdout, Duration: out.Duration, ExitCode: out.ExitCode, Truncated: out.Truncated}

	res := &Result{
		Data:    d,
		Summary: fmt.Sprintf("osd.%d 共 %d 个 perf counter，op_w_latency=%.4f，commit_latency_ms=%.2f", id, len(d.Avg), d.Avg["op_w_latency"], d.Avg["commit_latency_ms"]),
	}
	if d.Avg["commit_latency_ms"] > 50 {
		res.Hints = append(res.Hints, "commit 延迟偏高（>50ms），通常指向底层磁盘或网络问题，可调用 host_io 进一步确认")
	}
	return res, raw, nil
}
```

- [ ] **Step 7: 写 host_io 失败测试**

`pkg/doctor/tools/host_io_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

func TestHostIORunsIostatDmesgSmart(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"iostat", "-x", "1", "3"}, "Device  r/s  w/s  r_await  w_await  %util\nsdc  10  200  5.2  420.1  99.8\n")
	tr.on("node-3", []string{"dmesg", "-T"}, "[Fri Oct 23 15:00:00 2026] I/O error, dev sdc, sector 123\n")
	tr.on("node-3", []string{"smartctl", "-a", "/dev/sdc"}, "SMART overall-health self-assessment test result: FAILED\n")

	res, raw, err := NewHostIO(tr).Execute(context.Background(), json.RawMessage(`{"node":"node-3","device":"sdc"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*HostIOData)
	if d.Iostat == "" {
		t.Error("iostat missing")
	}
	if !strings.Contains(d.Dmesg, "I/O error") {
		t.Errorf("dmesg = %q", d.Dmesg)
	}
	if !strings.Contains(d.Smart, "FAILED") {
		t.Errorf("smart = %q", d.Smart)
	}
	if !strings.Contains(res.Summary, "sdc") {
		t.Errorf("summary = %q", res.Summary)
	}
	if raw.Node != "node-3" {
		t.Errorf("raw node = %q", raw.Node)
	}
}

func TestHostIOFiltersDmesgForDevice(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"iostat", "-x", "1", "3"}, "x")
	tr.on("node-3", []string{"dmesg", "-T"}, "[t] I/O error, dev sda, sector 1\n[t] I/O error, dev sdc, sector 2\n")
	tr.on("node-3", []string{"smartctl", "-a", "/dev/sdc"}, "ok")

	res, _, err := NewHostIO(tr).Execute(context.Background(), json.RawMessage(`{"node":"node-3","device":"sdc"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*HostIOData)
	if strings.Contains(d.Dmesg, "sda") {
		t.Errorf("dmesg should be filtered to sdc only: %q", d.Dmesg)
	}
	if !strings.Contains(d.Dmesg, "sdc") {
		t.Errorf("expected sdc line: %q", d.Dmesg)
	}
}

func TestHostIOUnavailableTool(t *testing.T) {
	tr := newFakeRunner()
	tr.caps.HostTools["smartctl"] = false
	tr.on("node-3", []string{"iostat", "-x", "1", "3"}, "x")
	tr.on("node-3", []string{"dmesg", "-T"}, "")

	res, _, err := NewHostIO(tr).Execute(context.Background(), json.RawMessage(`{"node":"node-3","device":"sdc"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*HostIOData)
	if d.SmartAvailable {
		t.Error("smart should be marked unavailable")
	}
	joined := strings.Join(res.Hints, " ")
	if !strings.Contains(joined, "smartctl") {
		t.Errorf("expected hint about manual smartctl, got %v", res.Hints)
	}
}

func TestHostIORequiresNodeAndDevice(t *testing.T) {
	tr := newFakeRunner()
	for _, args := range []string{`{}`, `{"node":"node-3"}`, `{"device":"sdc"}`} {
		_, _, err := NewHostIO(tr).Execute(context.Background(), json.RawMessage(args))
		if err == nil {
			t.Errorf("expected error for args %s", args)
		}
	}
}

func TestHostIORejectsUnknownNode(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewHostIO(tr).Execute(context.Background(), json.RawMessage(`{"node":"node-99","device":"sdc"}`))
	if err == nil {
		t.Fatal("expected error for unknown node")
	}
	var te *access.ToolError
	_ = te
}
```

- [ ] **Step 8: 运行测试确认失败**

Run: `go test ./pkg/doctor/tools/ -run TestHostIO -v`
Expected: FAIL — `undefined: NewHostIO`

- [ ] **Step 9: 实现 host_io**

`pkg/doctor/tools/host_io.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
)

// HostIOData 是 host_io 的结构化结果。
type HostIOData struct {
	Node           string `json:"node"`
	Device         string `json:"device"`
	Iostat         string `json:"iostat"`
	Dmesg          string `json:"dmesg"`
	Smart          string `json:"smart,omitempty"`
	SmartAvailable bool   `json:"smart_available"`
}

type hostIOTool struct{ r Runner }

func NewHostIO(r Runner) Tool { return &hostIOTool{r: r} }

func (t *hostIOTool) Name() string { return "host_io" }

func (t *hostIOTool) Description() string {
	return "在指定节点的指定物理盘上采集 iostat、与磁盘相关的 dmesg 错误、SMART 健康状态。用于确认磁盘是否真的有问题。必须给出节点名和盘名（如 sdc）。"
}

func (t *hostIOTool) Schema() json.RawMessage {
	return json.RawMessage(`{
      "type":"object",
      "properties":{
        "node":{"type":"string","description":"节点名，来自 osd_detail 或 osd_tree"},
        "device":{"type":"string","description":"物理盘名，如 sdc"}
      },
      "required":["node","device"],
      "additionalProperties":false
    }`)
}

func (t *hostIOTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		Node   string `json:"node"`
		Device string `json:"device"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	if in.Node == "" || in.Device == "" {
		return nil, nil, fmt.Errorf("必须同时提供 node 与 device")
	}
	if !knownNode(t.r, in.Node) {
		return nil, nil, &access.ToolError{Kind: access.KindUnreachable, Message: fmt.Sprintf("未知节点 %q", in.Node)}
	}

	caps := t.r.Capabilities(ctx, in.Node)
	d := &HostIOData{Node: in.Node, Device: normalizeDevice(in.Device), SmartAvailable: caps.HostTools["smartctl"]}
	devPath := "/dev/" + d.Device

	var raw *Raw

	if caps.HostTools["iostat"] {
		if out, err := t.r.Run(ctx, in.Node, []string{"iostat", "-x", "1", "3"}); err == nil {
			d.Iostat = filterDeviceLines(out.Stdout, d.Device)
			raw = &Raw{Node: in.Node, Command: out.Command, Output: out.Stdout, Duration: out.Duration, ExitCode: out.ExitCode, Truncated: out.Truncated}
		}
	}

	if out, err := t.r.Run(ctx, in.Node, []string{"dmesg", "-T"}); err == nil {
		d.Dmesg = filterDeviceLines(out.Stdout, d.Device)
		if raw == nil {
			raw = &Raw{Node: in.Node, Command: out.Command, Output: out.Stdout, Duration: out.Duration, ExitCode: out.ExitCode, Truncated: out.Truncated}
		}
	}

	res := &Result{
		Data: d,
	}
	if d.SmartAvailable {
		if out, err := t.r.Run(ctx, in.Node, []string{"smartctl", "-a", devPath}); err == nil {
			d.Smart = out.Stdout
			if raw == nil {
				raw = &Raw{Node: in.Node, Command: out.Command, Output: out.Stdout, Duration: out.Duration, ExitCode: out.ExitCode, Truncated: out.Truncated}
			}
		}
	} else {
		res.Hints = append(res.Hints, fmt.Sprintf("节点 %s 没有 smartctl，无法读取 SMART；建议人工执行 smartctl -a %s", in.Node, devPath))
	}

	res.Summary = fmt.Sprintf("%s/%s：%s", in.Node, d.Device, ioVerdict(d))
	if raw == nil {
		raw = &Raw{Node: in.Node, Command: "iostat/dmesg/smartctl"}
	}
	return res, raw, nil
}

// normalizeDevice 把 /dev/sdc 归一为 sdc。
func normalizeDevice(dev string) string {
	return strings.TrimPrefix(strings.TrimSpace(dev), "/dev/")
}

// filterDeviceLines 只保留提到该设备的行（含表头）。
func filterDeviceLines(out, device string) string {
	var kept []string
	for _, line := range strings.Split(out, "
") {
		if strings.Contains(line, device) || strings.Contains(line, "Device") || strings.Contains(line, "Linux version") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "
")
}

func ioVerdict(d *HostIOData) string {
	var parts []string
	if strings.Contains(d.Iostat, "100.0") || strings.Contains(d.Iostat, "99.") {
		parts = append(parts, "iostat 显示磁盘利用率接近 100%")
	}
	if strings.Contains(d.Dmesg, "I/O error") || strings.Contains(d.Dmesg, "Medium Error") {
		parts = append(parts, "dmesg 有 I/O 错误")
	}
	if strings.Contains(d.Smart, "FAILED") {
		parts = append(parts, "SMART 自检失败")
	}
	if len(parts) == 0 {
		return "未发现明显的磁盘层异常"
	}
	return strings.Join(parts, "；")
}

func knownNode(r Runner, node string) bool {
	for _, n := range r.Nodes() {
		if n == node {
			return true
		}
	}
	return false
}
```

- [ ] **Step 10: 运行测试确认通过**

Run: `go test ./pkg/doctor/tools/ -run 'TestSlowOps|TestPerfCounters|TestHostIO' -v`
Expected: PASS

- [ ] **Step 11: 提交**

```bash
git add pkg/doctor/tools/slow_ops.go pkg/doctor/tools/slow_ops_test.go \
        pkg/doctor/tools/perf_counters.go pkg/doctor/tools/perf_counters_test.go \
        pkg/doctor/tools/host_io.go pkg/doctor/tools/host_io_test.go
git commit -m "feat(doctor/tools): add slow_ops, perf_counters and host_io tools"
```

---
### Task 14: read_log、host_check 与 ceph_source_lookup 工具

**Files:**
- Create: `pkg/doctor/tools/read_log.go`
- Create: `pkg/doctor/tools/host_check.go`
- Create: `pkg/doctor/tools/source_lookup.go`
- Test: `pkg/doctor/tools/read_log_test.go`
- Test: `pkg/doctor/tools/host_check_test.go`
- Test: `pkg/doctor/tools/source_lookup_test.go`

- [ ] **Step 1: 写 read_log 失败测试**

`pkg/doctor/tools/read_log_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const sampleLog = `2026-10-23 15:00:00.000 osd.7 heartbeat_check: no reply
2026-10-23 15:00:01.000 osd.7 slow request 30s
2026-10-23 15:00:02.000 osd.7 heartbeat_check: no reply
`

func TestReadLogTail(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"tail", "-n", "500", "/var/log/ceph/fsid/ceph-osd.7.log"}, sampleLog)

	res, raw, err := NewReadLog(tr).Execute(context.Background(), json.RawMessage(
		`{"node":"node-3","path":"/var/log/ceph/fsid/ceph-osd.7.log","tail_lines":500}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*ReadLogData)
	if d.Matched != 3 {
		t.Errorf("matched = %d", d.Matched)
	}
	if !strings.Contains(d.Content, "heartbeat_check") {
		t.Errorf("content = %q", d.Content)
	}
	if raw.Command == "" {
		t.Error("raw missing")
	}
}

func TestReadLogGrepCountsAndKeepsMatches(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"tail", "-n", "1000", "/var/log/ceph/fsid/ceph-osd.7.log"}, sampleLog)
	tr.on("node-3", []string{"grep", "-F", "-c", "heartbeat_check", "/var/log/ceph/fsid/ceph-osd.7.log"}, "2\n")

	res, _, err := NewReadLog(tr).Execute(context.Background(), json.RawMessage(
		`{"node":"node-3","path":"/var/log/ceph/fsid/ceph-osd.7.log","grep":"heartbeat_check"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*ReadLogData)
	if d.MatchCount != 2 {
		t.Errorf("match count = %d", d.MatchCount)
	}
	if d.Matched != 2 {
		t.Errorf("filtered lines = %d", d.Matched)
	}
}

func TestReadLogRejectsPathOutsideWhitelist(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewReadLog(tr).Execute(context.Background(), json.RawMessage(
		`{"node":"node-3","path":"/etc/shadow"}`))
	if err == nil {
		t.Fatal("expected rejection for path outside whitelist")
	}
}

func TestReadLogRejectsTraversal(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewReadLog(tr).Execute(context.Background(), json.RawMessage(
		`{"node":"node-3","path":"/var/log/ceph/../../../etc/passwd"}`))
	if err == nil {
		t.Fatal("expected rejection for traversal")
	}
}

func TestReadLogCapsLines(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"tail", "-n", "5000", "/var/log/ceph/a.log"}, "x")

	_, _, err := NewReadLog(tr).Execute(context.Background(), json.RawMessage(
		`{"node":"node-3","path":"/var/log/ceph/a.log","tail_lines":999999}`))
	if err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/tools/ -run TestReadLog -v`
Expected: FAIL — `undefined: NewReadLog`

- [ ] **Step 3: 实现 read_log**

`pkg/doctor/tools/read_log.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ReadLogData 是 read_log 的结构化结果。
type ReadLogData struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Matched    int    `json:"matched"`
	MatchCount int    `json:"match_count,omitempty"`
	Truncated  bool   `json:"truncated"`
}

type readLogTool struct{ r Runner }

func NewReadLog(r Runner) Tool { return &readLogTool{r: r} }

func (t *readLogTool) Name() string { return "read_log" }

func (t *readLogTool) Description() string {
	return "读取指定节点上白名单目录内的日志文件。支持读取尾部若干行，或按关键字过滤。日志路径只能位于 /var/log/ceph 或 /var/lib/rook 下。"
}

func (t *readLogTool) Schema() json.RawMessage {
	return json.RawMessage(`{
      "type":"object",
      "properties":{
        "node":{"type":"string","description":"节点名"},
        "path":{"type":"string","description":"日志文件绝对路径，必须在 /var/log/ceph 或 /var/lib/rook 下"},
        "tail_lines":{"type":"integer","description":"读取尾部行数，默认 500，最大 5000"},
        "grep":{"type":"string","description":"可选，只保留包含该字符串的行（区分大小写）"}
      },
      "required":["node","path"],
      "additionalProperties":false
    }`)
}

const maxLogLines = 5000

func (t *readLogTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		Node      string `json:"node"`
		Path      string `json:"path"`
		TailLines int    `json:"tail_lines"`
		Grep      string `json:"grep"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	if in.Node == "" || in.Path == "" {
		return nil, nil, fmt.Errorf("必须提供 node 与 path")
	}
	if !knownNode(t.r, in.Node) {
		return nil, nil, fmt.Errorf("未知节点 %q", in.Node)
	}
	if !pathWhitelisted(in.Path) {
		return nil, nil, fmt.Errorf("路径 %q 不在白名单内（仅允许 /var/log/ceph 与 /var/lib/rook）", in.Path)
	}
	if in.TailLines <= 0 {
		in.TailLines = 500
	}
	if in.TailLines > maxLogLines {
		in.TailLines = maxLogLines
	}

	out, err := t.r.Run(ctx, in.Node, []string{"tail", "-n", strconv.Itoa(in.TailLines), in.Path})
	if err != nil {
		return nil, nil, err
	}

	d := &ReadLogData{Path: in.Path}
	lines := strings.Split(strings.TrimRight(out.Stdout, "\n"), "\n")
	total := len(lines)
	if in.Grep != "" {
		var kept []string
		for _, l := range lines {
			if strings.Contains(l, in.Grep) {
				kept = append(kept, l)
			}
		}
		d.Matched = len(kept)
		d.MatchCount = len(kept)
		d.Content = strings.Join(kept, "\n")
	} else {
		d.Matched = total
		d.Content = out.Stdout
	}

	raw := &Raw{Node: in.Node, Command: out.Command, Output: out.Stdout, Duration: out.Duration, ExitCode: out.ExitCode, Truncated: out.Truncated}

	res := &Result{
		Data:    d,
		Summary: fmt.Sprintf("%s：读取 %d 行，匹配 %d 行", in.Path, in.TailLines, d.Matched),
	}
	if in.Grep != "" && d.Matched == 0 {
		res.Hints = append(res.Hints, fmt.Sprintf("在最近 %d 行中未找到 %q，可增大 tail_lines 或换用更短的关键字", in.TailLines, in.Grep))
	}
	return res, raw, nil
}

// pathWhitelisted 与 access.Policy 的前缀规则保持一致。
func pathWhitelisted(p string) bool {
	if strings.Contains(p, "..") {
		return false
	}
	for _, prefix := range []string{"/var/log/ceph", "/var/lib/rook"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: 写 host_check 失败测试**

`pkg/doctor/tools/host_check_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestHostCheckCollectsBasics(t *testing.T) {
	tr := newFakeRunner()
	tr.on("node-3", []string{"uptime"}, " 15:00:00 up 30 days, load average: 12.5, 11.0, 9.8\n")
	tr.on("node-3", []string{"free", "-m"}, "              total used free\nMem:          64000 61000 3000\n")
	tr.on("node-3", []string{"df", "-h"}, "Filesystem Size Used Avail Use% Mounted on\n/dev/sda1 100G 95G 5G 95% /\n")

	res, raw, err := NewHostCheck(tr).Execute(context.Background(), json.RawMessage(`{"node":"node-3"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*HostCheckData)
	if !strings.Contains(d.Load, "12.5") {
		t.Errorf("load = %q", d.Load)
	}
	if !strings.Contains(d.Memory, "61000") {
		t.Errorf("memory = %q", d.Memory)
	}
	if raw.Node != "node-3" {
		t.Errorf("raw node = %q", raw.Node)
	}
	// 高负载应给出提示
	if !strings.Contains(strings.Join(res.Hints, " "), "负载") {
		t.Errorf("expected load hint, got %v", res.Hints)
	}
}

func TestHostCheckRequiresNode(t *testing.T) {
	tr := newFakeRunner()
	_, _, err := NewHostCheck(tr).Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 5: 运行测试确认失败**

Run: `go test ./pkg/doctor/tools/ -run TestHostCheck -v`
Expected: FAIL — `undefined: NewHostCheck`

- [ ] **Step 6: 实现 host_check**

`pkg/doctor/tools/host_check.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// HostCheckData 是 host_check 的结构化结果。
type HostCheckData struct {
	Node   string `json:"node"`
	Load   string `json:"load"`
	Memory string `json:"memory"`
	Disk   string `json:"disk"`
	Load1  float64 `json:"load1"`
}

type hostCheckTool struct{ r Runner }

func NewHostCheck(r Runner) Tool { return &hostCheckTool{r: r} }

func (t *hostCheckTool) Name() string { return "host_check" }

func (t *hostCheckTool) Description() string {
	return "在指定节点采集系统负载、内存、文件系统使用率。用于判断 OSD 变慢是否由宿主机整体资源紧张导致。"
}

func (t *hostCheckTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"node":{"type":"string","description":"节点名"}},"required":["node"],"additionalProperties":false}`)
}

func (t *hostCheckTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		Node string `json:"node"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	if in.Node == "" {
		return nil, nil, fmt.Errorf("必须提供 node")
	}
	if !knownNode(t.r, in.Node) {
		return nil, nil, fmt.Errorf("未知节点 %q", in.Node)
	}

	d := &HostCheckData{Node: in.Node}
	var raw *Raw

	if out, err := t.r.Run(ctx, in.Node, []string{"uptime"}); err == nil {
		d.Load = strings.TrimSpace(out.Stdout)
		d.Load1 = parseLoad1(out.Stdout)
		raw = &Raw{Node: in.Node, Command: out.Command, Output: out.Stdout, Duration: out.Duration, ExitCode: out.ExitCode}
	}
	if out, err := t.r.Run(ctx, in.Node, []string{"free", "-m"}); err == nil {
		d.Memory = out.Stdout
		if raw == nil {
			raw = &Raw{Node: in.Node, Command: out.Command, Output: out.Stdout, Duration: out.Duration, ExitCode: out.ExitCode}
		}
	}
	if out, err := t.r.Run(ctx, in.Node, []string{"df", "-h"}); err == nil {
		d.Disk = out.Stdout
		if raw == nil {
			raw = &Raw{Node: in.Node, Command: out.Command, Output: out.Stdout, Duration: out.Duration, ExitCode: out.ExitCode}
		}
	}
	if raw == nil {
		raw = &Raw{Node: in.Node, Command: "uptime/free/df"}
	}

	res := &Result{Data: d, Summary: fmt.Sprintf("%s：%s", in.Node, d.Load)}
	if d.Load1 > 16 {
		res.Hints = append(res.Hints, "节点负载较高，可能是 OSD 变慢的上游原因；可继续用 host_io 确认具体磁盘")
	}
	if strings.Contains(d.Disk, " 9") || strings.Contains(d.Disk, "100%") {
		res.Hints = append(res.Hints, "存在使用率很高的文件系统，检查是否影响 OSD 数据盘")
	}
	return res, raw, nil
}

// parseLoad1 从 uptime 输出解析 1 分钟负载。
func parseLoad1(out string) float64 {
	idx := strings.Index(out, "load average:")
	if idx < 0 {
		return 0
	}
	rest := strings.TrimSpace(out[idx+len("load average:"):])
	first := strings.TrimSpace(strings.Split(rest, ",")[0])
	v, _ := strconv.ParseFloat(first, 64)
	return v
}
```

- [ ] **Step 7: 写 source_lookup 失败测试**

`pkg/doctor/tools/source_lookup_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceLookupFindsString(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "v18.2.4", "src", "osd")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "void OSD::log_slow_op() {\n  ss << \"slow request \" << duration;\n}\n"
	if err := os.WriteFile(filepath.Join(src, "OSD.cc"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tr := newFakeRunner()
	tr.caps.CephVersion = "18.2.4"
	tool := NewSourceLookup(tr, dir)

	res, raw, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"slow request"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*SourceLookupData)
	if len(d.Matches) == 0 {
		t.Fatal("expected matches")
	}
	if !strings.Contains(d.Matches[0].File, "OSD.cc") {
		t.Errorf("file = %q", d.Matches[0].File)
	}
	if d.Matches[0].Line == 0 {
		t.Error("line should be set")
	}
	if raw.Output == "" {
		t.Error("raw should carry the grep output")
	}
}

func TestSourceLookupNoMatch(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "v18.2.4"), 0o755)

	tr := newFakeRunner()
	tool := NewSourceLookup(tr, dir)
	res, _, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"nonexistent_string_xyz"}`))
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(*SourceLookupData)
	if len(d.Matches) != 0 {
		t.Errorf("expected no matches, got %+v", d.Matches)
	}
}

func TestSourceLookupUnavailableWithoutDir(t *testing.T) {
	tr := newFakeRunner()
	tool := NewSourceLookup(tr, "")
	_, _, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"x"}`))
	if err == nil {
		t.Fatal("expected unavailable error when source dir not configured")
	}
}

func TestSourceLookupRejectsShortPattern(t *testing.T) {
	dir := t.TempDir()
	tr := newFakeRunner()
	tool := NewSourceLookup(tr, dir)
	_, _, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"a"}`))
	if err == nil {
		t.Fatal("expected error for too-short pattern")
	}
}
```

- [ ] **Step 8: 运行测试确认失败**

Run: `go test ./pkg/doctor/tools/ -run TestSourceLookup -v`
Expected: FAIL — `undefined: NewSourceLookup`

- [ ] **Step 9: 实现 source_lookup**

`pkg/doctor/tools/source_lookup.go`：

```go
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SourceMatch 一处源码命中。
type SourceMatch struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}

// SourceLookupData 是 ceph_source_lookup 的结构化结果。
type SourceLookupData struct {
	Pattern string        `json:"pattern"`
	Version string        `json:"version"`
	Matches []SourceMatch `json:"matches"`
}

type sourceLookupTool struct {
	r      Runner
	srcDir string
}

func NewSourceLookup(r Runner, srcDir string) Tool {
	return &sourceLookupTool{r: r, srcDir: srcDir}
}

func (t *sourceLookupTool) Name() string { return "ceph_source_lookup" }

func (t *sourceLookupTool) Description() string {
	return "在本地 Ceph 源码目录中按日志片段检索，定位该日志字符串在源码中的文件与行号。用于理解某条日志的确切触发条件。仅当配置了源码目录时可用。"
}

func (t *sourceLookupTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"日志中的特征字符串，至少 6 个字符"}},"required":["pattern"],"additionalProperties":false}`)
}

func (t *sourceLookupTool) Execute(ctx context.Context, args json.RawMessage) (*Result, *Raw, error) {
	var in struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, nil, fmt.Errorf("参数解析失败: %w", err)
	}
	if len(in.Pattern) < 6 {
		return nil, nil, fmt.Errorf("pattern 太短（至少 6 个字符），否则会命中大量无关代码")
	}
	if t.srcDir == "" {
		return nil, nil, fmt.Errorf("未配置 Ceph 源码目录（--ceph-src），源码反查不可用")
	}

	version := t.r.Capabilities(ctx, primaryNode(t.r)).CephVersion
	dir, err := resolveSourceDir(t.srcDir, version)
	if err != nil {
		return nil, nil, err
	}

	matches, searchOut, err := searchSource(ctx, dir, in.Pattern, 5)
	if err != nil {
		return nil, nil, err
	}

	d := &SourceLookupData{Pattern: in.Pattern, Version: version, Matches: matches}
	raw := &Raw{Node: "local", Command: fmt.Sprintf("grep -r %q %s", in.Pattern, dir), Output: searchOut}

	res := &Result{Data: d}
	if len(matches) == 0 {
		res.Summary = fmt.Sprintf("在 %s 源码中未找到 %q", version, in.Pattern)
		return res, raw, nil
	}
	var where []string
	for _, m := range matches {
		where = append(where, fmt.Sprintf("%s:%d", m.File, m.Line))
	}
	res.Summary = fmt.Sprintf("在 %s 源码中找到 %d 处：%s", version, len(matches), strings.Join(where, "、"))
	res.Hints = append(res.Hints, "可结合源码上下文判断该日志的确切触发条件，再回到运行时数据验证")
	return res, raw, nil
}

// resolveSourceDir 在源码根目录下找到与版本匹配的子目录。
func resolveSourceDir(root, version string) (string, error) {
	candidates := []string{
		filepath.Join(root, "v"+version),
		filepath.Join(root, version),
		filepath.Join(root, "ceph-"+version),
		root,
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("在 %s 下找不到版本 %s 的源码目录", root, version)
}

// searchSource 递归检索源码，返回命中位置及逐行上下文。
func searchSource(ctx context.Context, dir, pattern string, maxHits int) ([]SourceMatch, string, error) {
	var matches []SourceMatch
	var sb strings.Builder

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过不可读文件
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !isSourceFile(path) {
			return nil
		}
		if len(matches) >= maxHits {
			return filepath.SkipAll
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		f, ferr := os.Open(path)
		if ferr != nil {
			return nil
		}
		defer f.Close()

		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineno := 0
		for sc.Scan() {
			lineno++
			line := sc.Text()
			if !strings.Contains(line, pattern) {
				continue
			}
			rel, _ := filepath.Rel(dir, path)
			matches = append(matches, SourceMatch{
				File:    rel,
				Line:    lineno,
				Snippet: strings.TrimSpace(line),
			})
			fmt.Fprintf(&sb, "%s:%d:%s\n", rel, lineno, strings.TrimSpace(line))
			if len(matches) >= maxHits {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return nil, "", fmt.Errorf("检索源码失败: %w", err)
	}
	return matches, sb.String(), nil
}

func isSourceFile(path string) bool {
	switch filepath.Ext(path) {
	case ".cc", ".h", ".c", ".py", ".hpp", ".cxx":
		return true
	}
	return false
}
```

- [ ] **Step 10: 删除占位实现**

`placeholders.go` 中这 12 个构造尝试在 Task 9-14 中已逐个被真实实现取代。确认文件里已无残留后，直接删除该文件：

```bash
rm pkg/doctor/tools/placeholders.go
```

若编译报重复定义，说明还有构造函数未实现，先补齐再删。

- [ ] **Step 11: 运行全部工具测试**

Run: `go test ./pkg/doctor/tools/ -v`
Expected: PASS（包括 `TestDefaultRegistryHasTwelveTools`）

- [ ] **Step 12: 运行 race 检测**

Run: `go test -race ./pkg/doctor/tools/`
Expected: PASS

- [ ] **Step 13: 提交**

```bash
git add pkg/doctor/tools/read_log.go pkg/doctor/tools/read_log_test.go \
        pkg/doctor/tools/host_check.go pkg/doctor/tools/host_check_test.go \
        pkg/doctor/tools/source_lookup.go pkg/doctor/tools/source_lookup_test.go \
        pkg/doctor/tools/registry.go
git commit -m "feat(doctor/tools): add read_log, host_check and source_lookup tools"
```

---
# M3：llm 与 loop

### Task 15: LLM 类型与 OpenAI 客户端

**Files:**
- Create: `pkg/doctor/llm/types.go`
- Create: `pkg/doctor/llm/client.go`
- Test: `pkg/doctor/llm/client_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/llm/client_test.go`：

```go
package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientSendsToolsAndParsesToolCalls(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
          "choices":[{"message":{
            "role":"assistant",
            "content":null,
            "tool_calls":[{"id":"call_1","type":"function","function":{"name":"ceph_status","arguments":"{}"}}]
          }}],
          "usage":{"prompt_tokens":100,"completion_tokens":20}
        }`))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "test-key", Model: "gpt-4o"})
	reply, err := c.Chat(context.Background(),
		[]Message{{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "hi"}},
		[]ToolSpec{{Name: "ceph_status", Description: "d", Schema: json.RawMessage(`{"type":"object"}`)}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d", len(reply.ToolCalls))
	}
	if reply.ToolCalls[0].Name != "ceph_status" {
		t.Errorf("name = %q", reply.ToolCalls[0].Name)
	}
	if reply.ToolCalls[0].ID != "call_1" {
		t.Errorf("id = %q", reply.ToolCalls[0].ID)
	}
	if reply.Usage.PromptTokens != 100 {
		t.Errorf("usage = %+v", reply.Usage)
	}

	// 请求体应包含 tools 定义
	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools not sent: %+v", gotBody["tools"])
	}
	if gotBody["model"] != "gpt-4o" {
		t.Errorf("model = %v", gotBody["model"])
	}
}

func TestClientParsesTextReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"结论是X"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	reply, err := c.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Content != "结论是X" {
		t.Errorf("content = %q", reply.Content)
	}
	if len(reply.ToolCalls) != 0 {
		t.Errorf("expected no tool calls")
	}
}

func TestClientReturnsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	_, err := c.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestClientRetriesOn500(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(500)
			w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxRetries: 3})
	c.retryDelay = 0
	reply, err := c.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Content != "ok" {
		t.Errorf("content = %q", reply.Content)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestToolResultMessageShape(t *testing.T) {
	m := ToolResultMessage("call_1", `{"summary":"ok"}`)
	if m.Role != RoleTool {
		t.Errorf("role = %q", m.Role)
	}
	if m.ToolCallID != "call_1" {
		t.Errorf("tool_call_id = %q", m.ToolCallID)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/llm/ -v`
Expected: FAIL — `undefined: NewClient`

- [ ] **Step 3: 实现 types.go**

`pkg/doctor/llm/types.go`：

```go
package llm

import (
	"context"
	"encoding/json"
)

// Role 消息角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message 一条对话消息。
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall 模型请求调用某个工具。
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"arguments"` // 原始 JSON 字符串
}

// ToolSpec 提供给模型的工具定义。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// Usage 令牌用量。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// Reply 模型的一次回复。
type Reply struct {
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
}

// ToolResultMessage 构造一条工具结果消息。
func ToolResultMessage(callID, content string) Message {
	return Message{Role: RoleTool, Content: content, ToolCallID: callID}
}

// ChatModel 是循环对模型的唯一依赖，便于测试替换。
type ChatModel interface {
	Chat(ctx context.Context, msgs []Message, tools []ToolSpec) (Reply, error)
}
```

- [ ] **Step 4: 写 client.go**

`pkg/doctor/llm/client.go`：

```go
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config OpenAI 兼容客户端配置。
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	Temperature float64
	MaxTokens  int
	Timeout    time.Duration
	MaxRetries int
}

// Client 调用 OpenAI 兼容的 /chat/completions。
type Client struct {
	cfg        Config
	http       *http.Client
	retryDelay time.Duration
}

func NewClient(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Model == "" {
		cfg.Model = "gpt-4o"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2
	}
	return &Client{
		cfg:        cfg,
		http:       &http.Client{Timeout: cfg.Timeout},
		retryDelay: 2 * time.Second,
	}
}

type wireMessage struct {
	Role       string          `json:"role"`
	Content    any             `json:"content,omitempty"`
	ToolCalls  []wireToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireRequest struct {
	Model       string        `json:"model"`
	Messages    []wireMessage `json:"messages"`
	Tools       []wireTool    `json:"tools,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type wireResponse struct {
	Choices []struct {
		Message struct {
			Role      string         `json:"role"`
			Content   *string        `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat 发送一轮对话。
func (c *Client) Chat(ctx context.Context, msgs []Message, tools []ToolSpec) (Reply, error) {
	req := c.buildRequest(msgs, tools)
	body, err := json.Marshal(req)
	if err != nil {
		return Reply{}, fmt.Errorf("marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 && c.retryDelay > 0 {
			select {
			case <-ctx.Done():
				return Reply{}, ctx.Err()
			case <-time.After(c.retryDelay):
			}
		}
		reply, retriable, err := c.do(ctx, body)
		if err == nil {
			return reply, nil
		}
		lastErr = err
		if !retriable {
			return Reply{}, err
		}
	}
	return Reply{}, fmt.Errorf("after %d attempts: %w", c.cfg.MaxRetries+1, lastErr)
}

func (c *Client) buildRequest(msgs []Message, tools []ToolSpec) wireRequest {
	wm := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		item := wireMessage{Role: string(m.Role), ToolCallID: m.ToolCallID}
		if m.Content != "" {
			item.Content = m.Content
		}
		for _, tc := range m.ToolCalls {
			wc := wireToolCall{ID: tc.ID, Type: "function"}
			wc.Function.Name = tc.Name
			wc.Function.Arguments = tc.Args
			item.ToolCalls = append(item.ToolCalls, wc)
		}
		wm = append(wm, item)
	}

	req := wireRequest{
		Model:       c.cfg.Model,
		Messages:    wm,
		Temperature: c.cfg.Temperature,
		MaxTokens:   c.cfg.MaxTokens,
	}
	for _, t := range tools {
		wt := wireTool{Type: "function"}
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		wt.Function.Parameters = t.Schema
		req.Tools = append(req.Tools, wt)
	}
	return req
}

func (c *Client) do(ctx context.Context, body []byte) (Reply, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Reply{}, false, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return Reply{}, true, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 500 || resp.StatusCode == 429 {
		return Reply{}, true, fmt.Errorf("api error %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	if resp.StatusCode != 200 {
		return Reply{}, false, fmt.Errorf("api error %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var wr wireResponse
	if err := json.Unmarshal(raw, &wr); err != nil {
		return Reply{}, false, fmt.Errorf("decode response: %w", err)
	}
	if wr.Error != nil {
		return Reply{}, false, fmt.Errorf("api error: %s", wr.Error.Message)
	}
	if len(wr.Choices) == 0 {
		return Reply{}, false, fmt.Errorf("empty choices")
	}

	msg := wr.Choices[0].Message
	reply := Reply{Usage: Usage{PromptTokens: wr.Usage.PromptTokens, CompletionTokens: wr.Usage.CompletionTokens}}
	if msg.Content != nil {
		reply.Content = *msg.Content
	}
	for _, tc := range msg.ToolCalls {
		reply.ToolCalls = append(reply.ToolCalls, ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments,
		})
	}
	return reply, false, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./pkg/doctor/llm/ -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add pkg/doctor/llm/
git commit -m "feat(doctor/llm): add OpenAI-compatible tool-calling client"
```

---

### Task 16: 证据链

**Files:**
- Create: `pkg/doctor/loop/evidence.go`
- Test: `pkg/doctor/loop/evidence_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/loop/evidence_test.go`：

```go
package loop

import (
	"testing"
	"time"
)

func TestChainAssignsSequentialIDs(t *testing.T) {
	c := NewChain()
	e1 := c.Add(Evidence{Tool: "ceph_status", Summary: "ok"})
	e2 := c.Add(Evidence{Tool: "osd_tree", Summary: "ok"})
	if e1.ID != "ev-001" {
		t.Errorf("first id = %q", e1.ID)
	}
	if e2.ID != "ev-002" {
		t.Errorf("second id = %q", e2.ID)
	}
}

func TestChainGet(t *testing.T) {
	c := NewChain()
	added := c.Add(Evidence{Tool: "x"})
	got, ok := c.Get(added.ID)
	if !ok {
		t.Fatal("expected to find evidence")
	}
	if got.Tool != "x" {
		t.Errorf("tool = %q", got.Tool)
	}
	if _, ok := c.Get("ev-999"); ok {
		t.Error("should not find missing id")
	}
}

func TestChainAllPreservesOrder(t *testing.T) {
	c := NewChain()
	for _, name := range []string{"a", "b", "c"} {
		c.Add(Evidence{Tool: name})
	}
	all := c.All()
	if len(all) != 3 {
		t.Fatalf("got %d", len(all))
	}
	if all[0].Tool != "a" || all[2].Tool != "c" {
		t.Errorf("order broken: %+v", all)
	}
}

func TestChainValidateRefs(t *testing.T) {
	c := NewChain()
	e1 := c.Add(Evidence{Tool: "a"})
	c.Add(Evidence{Tool: "b"})

	if err := c.ValidateRefs([]string{e1.ID}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := c.ValidateRefs([]string{e1.ID, "ev-999"}); err == nil {
		t.Fatal("expected error for missing ref")
	}
}

func TestChainDetectsNoProgress(t *testing.T) {
	c := NewChain()
	if c.NoProgress() {
		t.Error("empty chain is not progress")
	}
	c.Add(Evidence{Tool: "a"})
	c.MarkRound(1)
	if c.NoProgress() {
		t.Error("just added evidence, should have progress")
	}
	c.MarkRound(2)
	if !c.NoProgress() {
		t.Error("expected no progress after 2 rounds without new evidence")
	}
	c.Add(Evidence{Tool: "b"})
	c.MarkRound(3)
	if c.NoProgress() {
		t.Error("new evidence should reset no-progress")
	}
}

func TestChainDedupsIdenticalCall(t *testing.T) {
	c := NewChain()
	c.Add(Evidence{Tool: "osd_detail", Args: map[string]any{"osd_id": 7}})
	if !c.SeenCall("osd_detail", map[string]any{"osd_id": 7}) {
		t.Error("expected identical call detected")
	}
	if c.SeenCall("osd_detail", map[string]any{"osd_id": 8}) {
		t.Error("different args should not match")
	}
}

func TestChainRecordsDuration(t *testing.T) {
	c := NewChain()
	e := c.Add(Evidence{Tool: "a", Duration: 3 * time.Second})
	if e.Duration != 3*time.Second {
		t.Error("duration not preserved")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/loop/ -run TestChain -v`
Expected: FAIL — `undefined: NewChain`

- [ ] **Step 3: 实现**

`pkg/doctor/loop/evidence.go`：

```go
package loop

import (
	"encoding/json"
	"fmt"
	"time"
)

// Evidence 一条可追溯的诊断证据。
type Evidence struct {
	ID        string         `json:"id"`
	Round     int            `json:"round"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args,omitempty"`
	Node      string         `json:"node,omitempty"`
	Command   string         `json:"command,omitempty"`
	StartedAt time.Time      `json:"started_at"`
	Duration  time.Duration  `json:"duration"`
	ExitCode  int            `json:"exit_code"`
	RawOutput string         `json:"raw_output,omitempty"`
	Summary   string         `json:"summary,omitempty"`
	ErrorKind string         `json:"error_kind,omitempty"`
	ErrorMsg  string         `json:"error_message,omitempty"`
}

// Chain 证据链。
type Chain struct {
	items    []Evidence
	seen     map[string]bool
	round    int
	lastAdd  int
}

func NewChain() *Chain {
	return &Chain{seen: map[string]bool{}}
}

// Add 追加一条证据并分配 ID。
func (c *Chain) Add(e Evidence) Evidence {
	e.ID = fmt.Sprintf("ev-%03d", len(c.items)+1)
	if e.StartedAt.IsZero() {
		e.StartedAt = time.Now()
	}
	c.items = append(c.items, e)
	c.lastAdd = c.round
	return e
}

// Get 按 ID 查找。
func (c *Chain) Get(id string) (Evidence, bool) {
	for _, e := range c.items {
		if e.ID == id {
			return e, true
		}
	}
	return Evidence{}, false
}

// All 返回全部证据（按加入顺序）。
func (c *Chain) All() []Evidence {
	return append([]Evidence{}, c.items...)
}

// ValidateRefs 校验引用的证据 ID 是否都存在。
func (c *Chain) ValidateRefs(ids []string) error {
	for _, id := range ids {
		if _, ok := c.Get(id); !ok {
			return fmt.Errorf("引用了不存在的证据 %q", id)
		}
	}
	return nil
}

// MarkRound 记录当前轮次，用于无进展判定。
func (c *Chain) MarkRound(round int) {
	c.round = round
}

// NoProgress 判断连续两轮是否都没有新增证据。
func (c *Chain) NoProgress() bool {
	return c.round-c.lastAdd >= 2
}

// SeenCall 判断相同工具 + 相同参数是否已调用过。
func (c *Chain) SeenCall(tool string, args map[string]any) bool {
	return c.seen[callKey(tool, args)]
}

// MarkCall 记录一次调用，用于去重。
func (c *Chain) MarkCall(tool string, args map[string]any) {
	c.seen[callKey(tool, args)] = true
}

func callKey(tool string, args map[string]any) string {
	b, _ := json.Marshal(args)
	return tool + "|" + string(b)
}

// Len 返回证据条数。
func (c *Chain) Len() int { return len(c.items) }
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/doctor/loop/ -run TestChain -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/doctor/loop/evidence.go pkg/doctor/loop/evidence_test.go
git commit -m "feat(doctor/loop): add evidence chain with dedup and progress tracking"
```

---
### Task 17: 结论 schema 与校验

**Files:**
- Create: `pkg/doctor/loop/conclusion.go`
- Test: `pkg/doctor/loop/conclusion_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/loop/conclusion_test.go`：

```go
package loop

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConclusionSchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(ConclusionSchema, &v); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if v["type"] != "object" {
		t.Errorf("type = %v", v["type"])
	}
	props := v["properties"].(map[string]any)
	for _, want := range []string{"severity", "headline", "impact", "evidence_refs", "ruled_out", "recommendations", "unknowns"} {
		if _, ok := props[want]; !ok {
			t.Errorf("schema missing property %q", want)
		}
	}
	required := v["required"].([]any)
	if len(required) != 7 {
		t.Errorf("required has %d entries, want 7", len(required))
	}
}

func TestParseConclusion(t *testing.T) {
	raw := `{
      "severity":"high",
      "headline":"osd.7 所在盘 sdc 出现 I/O 错误",
      "impact":"2/1440 PG 降级，无数据丢失风险",
      "evidence_refs":["ev-003","ev-005"],
      "ruled_out":[{"hypothesis":"网络问题","ref":"ev-001"}],
      "recommendations":[{"risk":"low","action":"观察 15 分钟","command":"ceph osd ok-to-stop 7"}],
      "unknowns":[{"what":"SMART 状态","why":"smartctl 不可用","how":"手工执行 smartctl -a /dev/sdc"}]
    }`
	c, err := ParseConclusion(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.Severity != "high" {
		t.Errorf("severity = %q", c.Severity)
	}
	if len(c.EvidenceRefs) != 2 {
		t.Errorf("refs = %v", c.EvidenceRefs)
	}
	if c.Recommendations[0].Command != "ceph osd ok-to-stop 7" {
		t.Errorf("command = %q", c.Recommendations[0].Command)
	}
}

func TestParseConclusionRejectsUnknownSeverity(t *testing.T) {
	raw := `{"severity":"catastrophic","headline":"x","impact":"y","evidence_refs":[],
             "ruled_out":[],"recommendations":[],"unknowns":[]}`
	if _, err := ParseConclusion(raw); err == nil {
		t.Fatal("expected error for unknown severity")
	}
}

func TestParseConclusionRequiresHeadline(t *testing.T) {
	raw := `{"severity":"high","headline":"","impact":"y","evidence_refs":[],
             "ruled_out":[],"recommendations":[],"unknowns":[]}`
	if _, err := ParseConclusion(raw); err == nil {
		t.Fatal("expected error for empty headline")
	}
}

func TestParseConclusionRejectsBadRecommendationRisk(t *testing.T) {
	raw := `{"severity":"high","headline":"x","impact":"y","evidence_refs":[],
             "ruled_out":[],"recommendations":[{"risk":"whatever","action":"a"}],"unknowns":[]}`
	if _, err := ParseConclusion(raw); err == nil {
		t.Fatal("expected error for invalid risk level")
	}
}

func TestParseConclusionTreatsEmptyUnknownsAsExplicit(t *testing.T) {
	raw := `{"severity":"low","headline":"x","impact":"y","evidence_refs":[],
             "ruled_out":[],"recommendations":[],"unknowns":[]}`
	c, err := ParseConclusion(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.Unknowns == nil {
		t.Error("unknowns should be a non-nil empty slice (explicit 'nothing missing')")
	}
}

func TestConcludeToolName(t *testing.T) {
	if ConcludeToolName != "submit_conclusion" {
		t.Errorf("tool name = %q", ConcludeToolName)
	}
	if !strings.Contains(string(ConclusionToolSpec().Schema), "severity") {
		t.Error("tool schema should describe severity")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/loop/ -run 'TestConclusion|TestParseConclusion|TestConcludeTool' -v`
Expected: FAIL — `undefined: ConclusionSchema`

- [ ] **Step 3: 实现**

`pkg/doctor/loop/conclusion.go`：

```go
package loop

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/doctor/llm"
)

// ConcludeToolName 是模型提交结论时调用的工具名。
const ConcludeToolName = "submit_conclusion"

// ConclusionSchema 强制模型以固定结构提交结论。
var ConclusionSchema = json.RawMessage(`{
  "type":"object",
  "properties":{
    "severity":{"type":"string","enum":["critical","high","medium","low","none"],
                "description":"问题严重程度；集群完全健康时为 none"},
    "headline":{"type":"string","description":"一句话结论，说明根因，不超过 100 字"},
    "impact":{"type":"string","description":"影响范围与数据安全评估"},
    "evidence_refs":{"type":"array","items":{"type":"string"},
                     "description":"支撑结论的证据 ID，如 ev-003；必须是已存在的证据"},
    "ruled_out":{"type":"array","description":"已排除的假设及对应证据",
      "items":{"type":"object","properties":{
        "hypothesis":{"type":"string"},"ref":{"type":"string"}},
        "required":["hypothesis","ref"],"additionalProperties":false}},
    "recommendations":{"type":"array","description":"建议操作，按风险从低到高",
      "items":{"type":"object","properties":{
        "risk":{"type":"string","enum":["low","medium","high"]},
        "action":{"type":"string"},
        "command":{"type":"string","description":"可直接复制执行的命令，可为空"}},
        "required":["risk","action"],"additionalProperties":false}},
    "unknowns":{"type":"array","description":"未能确定的项；全部确定时传空数组",
      "items":{"type":"object","properties":{
        "what":{"type":"string"},"why":{"type":"string"},"how":{"type":"string"}},
        "required":["what","why","how"],"additionalProperties":false}}
  },
  "required":["severity","headline","impact","evidence_refs","ruled_out","recommendations","unknowns"],
  "additionalProperties":false
}`)

// Conclusion 是模型给出的结构化结论。
type Conclusion struct {
	Severity        string           `json:"severity"`
	Headline        string           `json:"headline"`
	Impact          string           `json:"impact"`
	EvidenceRefs    []string         `json:"evidence_refs"`
	RuledOut        []RuledOut       `json:"ruled_out"`
	Recommendations []Recommendation `json:"recommendations"`
	Unknowns        []Unknown        `json:"unknowns"`
}

// RuledOut 一个被排除的假设。
type RuledOut struct {
	Hypothesis string `json:"hypothesis"`
	Ref        string `json:"ref"`
}

// Recommendation 一条建议操作。
type Recommendation struct {
	Risk    string `json:"risk"`
	Action  string `json:"action"`
	Command string `json:"command,omitempty"`
}

// Unknown 一项未能确定的信息。
type Unknown struct {
	What string `json:"what"`
	Why  string `json:"why"`
	How  string `json:"how"`
}

var validSeverity = map[string]bool{
	"critical": true, "high": true, "medium": true, "low": true, "none": true,
}

var validRisk = map[string]bool{"low": true, "medium": true, "high": true}

// ParseConclusion 解析并校验模型提交的结论 JSON。
func ParseConclusion(raw string) (*Conclusion, error) {
	var c Conclusion
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("结论 JSON 解析失败: %w", err)
	}
	if !validSeverity[c.Severity] {
		return nil, fmt.Errorf("非法的 severity %q，可选 critical/high/medium/low/none", c.Severity)
	}
	if strings.TrimSpace(c.Headline) == "" {
		return nil, fmt.Errorf("headline 不能为空")
	}
	if len(c.Headline) > 200 {
		return nil, fmt.Errorf("headline 过长（%d 字符），请精简到 100 字以内", len(c.Headline))
	}
	for i, r := range c.Recommendations {
		if !validRisk[r.Risk] {
			return nil, fmt.Errorf("recommendations[%d].risk 非法 %q，可选 low/medium/high", i, r.Risk)
		}
		if strings.TrimSpace(r.Action) == "" {
			return nil, fmt.Errorf("recommendations[%d].action 不能为空", i)
		}
	}
	// unknowns 必须显式给出（空数组表示"无"），避免被省略后报告缺块
	if c.Unknowns == nil {
		c.Unknowns = []Unknown{}
	}
	if c.EvidenceRefs == nil {
		c.EvidenceRefs = []string{}
	}
	if c.RuledOut == nil {
		c.RuledOut = []RuledOut{}
	}
	if c.Recommendations == nil {
		c.Recommendations = []Recommendation{}
	}
	return &c, nil
}

// ConclusionToolSpec 返回提交结论用的工具定义。
// 模型必须调用它而不是直接输出文本，从而保证结构可校验。
func ConclusionToolSpec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: ConcludeToolName,
		Description: "提交最终诊断结论。必须在调查完成（或预算耗尽）后调用一次。" +
			"所有 evidence_refs 必须是真实存在的证据 ID；不确定的内容写入 unknowns，" +
			"若认为没有不确定项则传空数组。",
		Schema: ConclusionSchema,
	}
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/doctor/loop/ -run 'TestConclusion|TestParseConclusion|TestConcludeTool' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/doctor/loop/conclusion.go pkg/doctor/loop/conclusion_test.go
git commit -m "feat(doctor/loop): add conclusion schema with strict validation"
```

---

### Task 18: 调查循环

**Files:**
- Create: `pkg/doctor/loop/loop.go`
- Test: `pkg/doctor/loop/loop_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/loop/loop_test.go`：

```go
package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/pkg/doctor/llm"
	"github.com/microyahoo/infra-agent/pkg/doctor/tools"
)

// scriptedModel 按脚本返回预设回复。
type scriptedModel struct {
	replies []llm.Reply
	calls   int
	seenMsgs [][]llm.Message
}

func (m *scriptedModel) Chat(_ context.Context, msgs []llm.Message, _ []llm.ToolSpec) (llm.Reply, error) {
	m.seenMsgs = append(m.seenMsgs, append([]llm.Message{}, msgs...))
	if m.calls >= len(m.replies) {
		return llm.Reply{}, fmt.Errorf("script exhausted after %d calls", m.calls)
	}
	r := m.replies[m.calls]
	m.calls++
	return r, nil
}

// stubTool 返回固定 Result。
type stubTool struct {
	name string
	res  *tools.Result
	err  error
	calls int
}

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Description() string { return "stub " + s.name }
func (s *stubTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (*tools.Result, *tools.Raw, error) {
	s.calls++
	if s.err != nil {
		return nil, nil, s.err
	}
	return s.res, &tools.Raw{Node: "node-3", Command: "cmd-" + s.name, Output: "raw-output"}, nil
}

func conclusionJSON(severity string, refs []string) string {
	b, _ := json.Marshal(map[string]any{
		"severity":        severity,
		"headline":        "测试结论",
		"impact":          "无影响",
		"evidence_refs":   refs,
		"ruled_out":       []any{},
		"recommendations": []any{map[string]any{"risk": "low", "action": "观察"}},
		"unknowns":        []any{},
	})
	return string(b)
}

func newTestLoop(t *testing.T, model llm.ChatModel, reg *tools.Registry) *SessionRunner {
	t.Helper()
	return NewRunner(RunnerConfig{
		Model:        model,
		Registry:     reg,
		SystemPrompt: "你是测试助手",
		MaxRounds:    5,
		MaxToolCalls: 10,
		MaxPerTool:   2,
	})
}

func TestLoopCompletesOnConclusion(t *testing.T) {
	reg := tools.NewRegistry()
	st := &stubTool{name: "probe", res: &tools.Result{Summary: "发现异常", Hints: []string{"继续查"}}}
	reg.Register(st)

	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "probe", Args: "{}"}}},
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: ConcludeToolName, Args: conclusionJSON("high", []string{"ev-001"})}}},
	}}

	r := newTestLoop(t, model, reg)
	res, err := r.Run(context.Background(), Task{Question: "分析集群"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Conclusion == nil {
		t.Fatal("expected conclusion")
	}
	if res.Conclusion.Severity != "high" {
		t.Errorf("severity = %q", res.Conclusion.Severity)
	}
	if res.Chain.Len() != 1 {
		t.Errorf("evidence = %d", res.Chain.Len())
	}
	if res.Rounds != 2 {
		t.Errorf("rounds = %d", res.Rounds)
	}
}

func TestLoopRejectsFabricatedEvidenceRef(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "probe", res: &tools.Result{Summary: "x"}})

	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: ConcludeToolName, Args: conclusionJSON("low", []string{"ev-999"})}}},
		{ToolCalls: []llm.ToolCall{{ID: "c3", Name: ConcludeToolName, Args: conclusionJSON("low", []string{})}}},
	}}

	r := newTestLoop(t, model, reg)
	res, err := r.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Conclusion == nil {
		t.Fatal("expected conclusion after retry")
	}
	if model.calls != 2 {
		t.Errorf("expected retry on fabricated ref, calls = %d", model.calls)
	}
}

func TestLoopForcesConclusionWhenRoundsExhausted(t *testing.T) {
	reg := tools.NewRegistry()
	st := &stubTool{name: "probe", res: &tools.Result{Summary: "x"}}
	reg.Register(st)

	var replies []llm.Reply
	for i := 0; i < 5; i++ {
		replies = append(replies, llm.Reply{ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("c%d", i), Name: "probe", Args: fmt.Sprintf(`{"i":%d}`, i)}}})
	}
	// 预算耗尽后，循环应追加一条强制收尾请求；模型这时给出结论
	replies = append(replies, llm.Reply{ToolCalls: []llm.ToolCall{{ID: "final", Name: ConcludeToolName, Args: conclusionJSON("medium", []string{"ev-001"})}}})

	model := &scriptedModel{replies: replies}
	r := newTestLoop(t, model, reg)
	res, err := r.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Conclusion == nil {
		t.Fatal("expected conclusion after forced wrap-up")
	}
	if !res.BudgetExhausted {
		t.Error("expected BudgetExhausted flag")
	}
}

func TestLoopStopsOnRepeatedIdenticalCall(t *testing.T) {
	reg := tools.NewRegistry()
	st := &stubTool{name: "probe", res: &tools.Result{Summary: "same"}}
	reg.Register(st)

	var replies []llm.Reply
	for i := 0; i < 6; i++ {
		replies = append(replies, llm.Reply{ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("c%d", i), Name: "probe", Args: "{}"}}})
	}
	replies = append(replies, llm.Reply{ToolCalls: []llm.ToolCall{{ID: "f", Name: ConcludeToolName, Args: conclusionJSON("low", nil)}}})

	model := &scriptedModel{replies: replies}
	r := newTestLoop(t, model, reg)
	res, err := r.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if st.calls >= 6 {
		t.Errorf("expected early stop on duplicate calls, calls = %d", st.calls)
	}
	if res.Conclusion == nil {
		t.Fatal("expected conclusion")
	}
}

func TestLoopRecordsToolErrorsAsEvidence(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "probe", err: fmt.Errorf("命令超时")})

	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "probe", Args: "{}"}}},
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: ConcludeToolName, Args: conclusionJSON("low", []string{"ev-001"})}}},
	}}

	r := newTestLoop(t, model, reg)
	res, err := r.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	all := res.Chain.All()
	if len(all) != 1 {
		t.Fatalf("evidence = %d", len(all))
	}
	if all[0].ErrorKind == "" {
		t.Error("tool error should be recorded on evidence")
	}
}

func TestLoopSendsSystemPrompt(t *testing.T) {
	reg := tools.NewRegistry()
	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: ConcludeToolName, Args: conclusionJSON("none", nil)}}},
	}}
	r := newTestLoop(t, model, reg)
	if _, err := r.Run(context.Background(), Task{Question: "q"}); err != nil {
		t.Fatal(err)
	}
	first := model.seenMsgs[0]
	if first[0].Role != llm.RoleSystem {
		t.Fatalf("first message role = %q", first[0].Role)
	}
	if !strings.Contains(first[0].Content, "你是测试助手") {
		t.Error("system prompt not sent")
	}
}

func TestCompressHistoryShrinksOldToolResults(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "q"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "a", Args: "{}"}}},
		{Role: llm.RoleTool, ToolCallID: "c1", Content: `{"evidence_id":"ev-001","summary":"` + strings.Repeat("长", 500) + `"}`},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c2", Name: "b", Args: "{}"}}},
		{Role: llm.RoleTool, ToolCallID: "c2", Content: `{"evidence_id":"ev-002","summary":"最近的结果"}`},
	}
	out := compressHistory(msgs, 1)
	if len(out) != len(msgs) {
		t.Fatalf("message count changed: %d", len(out))
	}
	if out[0].Content != "sys" || out[1].Content != "q" {
		t.Error("system/user messages must never be compressed")
	}
	if len(out[3].Content) >= len(msgs[3].Content) {
		t.Error("old tool result should be shortened")
	}
	if !strings.Contains(out[3].Content, "ev-001") {
		t.Error("compressed result must keep the evidence id for traceability")
	}
	if out[5].Content != msgs[5].Content {
		t.Error("recent tool result must stay intact")
	}
}

func TestLoopToolCallBudget(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "a", res: &tools.Result{Summary: "x"}})
	reg.Register(&stubTool{name: "b", res: &tools.Result{Summary: "y"}})

	// 单轮内塞 20 个调用，超过 MaxToolCalls=10
	var tcs []llm.ToolCall
	for i := 0; i < 20; i++ {
		name := "a"
		if i%2 == 0 {
			name = "b"
		}
		tcs = append(tcs, llm.ToolCall{ID: fmt.Sprintf("c%d", i), Name: name, Args: fmt.Sprintf(`{"i":%d}`, i)})
	}
	model := &scriptedModel{replies: []llm.Reply{
		{ToolCalls: tcs},
		{ToolCalls: []llm.ToolCall{{ID: "f", Name: ConcludeToolName, Args: conclusionJSON("low", nil)}}},
	}}

	r := newTestLoop(t, model, reg)
	res, err := r.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Chain.Len() > 10 {
		t.Errorf("tool calls exceeded budget: %d", res.Chain.Len())
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/loop/ -run TestLoop -v`
Expected: FAIL — `undefined: NewRunner`

- [ ] **Step 3: 实现**

`pkg/doctor/loop/loop.go`：

```go
package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/doctor/llm"
	"github.com/microyahoo/infra-agent/pkg/doctor/tools"
)

// Task 一次诊断任务。
type Task struct {
	ClusterID string
	Question  string
}

// RunnerConfig 循环参数。
type RunnerConfig struct {
	Model        llm.ChatModel
	Registry     *tools.Registry
	SystemPrompt string
	MaxRounds    int
	MaxToolCalls int
	MaxPerTool   int
	OnProgress   func(msg string)
}

// Result 一次诊断的完整产出。
type Result struct {
	Conclusion      *Conclusion
	Chain           *Chain
	Rounds          int
	ToolCalls       int
	BudgetExhausted bool
	Usage           llm.Usage
}

// SessionRunner 驱动模型与工具往返。
type SessionRunner struct {
	cfg RunnerConfig
}

func NewRunner(cfg RunnerConfig) *SessionRunner {
	if cfg.MaxRounds == 0 {
		cfg.MaxRounds = 20
	}
	if cfg.MaxToolCalls == 0 {
		cfg.MaxToolCalls = 60
	}
	if cfg.MaxPerTool == 0 {
		cfg.MaxPerTool = 5
	}
	return &SessionRunner{cfg: cfg}
}

// Run 执行调查循环，直到模型给出结论或预算耗尽。
func (r *SessionRunner) Run(ctx context.Context, task Task) (*Result, error) {
	if r.cfg.Model == nil {
		return nil, fmt.Errorf("未配置模型")
	}
	if r.cfg.Registry == nil {
		return nil, fmt.Errorf("未配置工具注册表")
	}

	toolSpecs := toLLMSpecs(r.cfg.Registry.Specs())
	toolSpecs = append(toolSpecs, ConclusionToolSpec())

	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: r.cfg.SystemPrompt},
		{Role: llm.RoleUser, Content: task.Question},
	}

	chain := NewChain()
	res := &Result{Chain: chain}
	perTool := map[string]int{}

	for round := 1; round <= r.cfg.MaxRounds; round++ {
		chain.MarkRound(round)
		res.Rounds = round
		r.progress(fmt.Sprintf("第 %d 轮", round))

		// 每轮调用模型前压缩历史，控制上下文增长
		if round > 3 {
			msgs = compressHistory(msgs, 6)
		}

		reply, err := r.cfg.Model.Chat(ctx, msgs, toolSpecs)
		if err != nil {
			return res, fmt.Errorf("模型调用失败: %w", err)
		}
		res.Usage.PromptTokens += reply.Usage.PromptTokens
		res.Usage.CompletionTokens += reply.Usage.CompletionTokens

		if len(reply.ToolCalls) == 0 {
			// 模型没调工具也没给结论 —— 视为协议错误，要求它按格式提交
			msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: reply.Content})
			msgs = append(msgs, llm.Message{
				Role:    llm.RoleUser,
				Content: "请调用 " + ConcludeToolName + " 提交结构化结论。",
			})
			if reply.Content != "" {
				// 尝试把纯文本兜底成结论
				if c, cerr := ParseConclusion(reply.Content); cerr == nil {
					res.Conclusion = c
					return res, nil
				}
			}
			continue
		}

		// 记录 assistant 的 tool_calls，工具结果才能正确配对
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: reply.ToolCalls})

		for _, tc := range reply.ToolCalls {
			// 结论提交
			if tc.Name == ConcludeToolName {
				c, cerr := ParseConclusion(tc.Args)
				if cerr != nil {
					msgs = append(msgs, llm.ToolResultMessage(tc.ID, "结论格式错误："+cerr.Error()+"。请修正后重新调用。"))
					continue
				}
				if verr := chain.ValidateRefs(c.EvidenceRefs); verr != nil {
					msgs = append(msgs, llm.ToolResultMessage(tc.ID, verr.Error()+"。只能用已采集到的证据 ID。"))
					continue
				}
				for _, ro := range c.RuledOut {
					if ro.Ref != "" {
						if verr := chain.ValidateRefs([]string{ro.Ref}); verr != nil {
							c = nil
							msgs = append(msgs, llm.ToolResultMessage(tc.ID, "ruled_out 引用了不存在的证据 "+ro.Ref))
							break
						}
					}
				}
				if c == nil {
					continue
				}
				msgs = append(msgs, llm.ToolResultMessage(tc.ID, "结论已接收。"))
				res.Conclusion = c
				res.ToolCalls = countToolCalls(chain)
				return res, nil
			}

			// 预算检查
			if res.Chain.Len() >= r.cfg.MaxToolCalls {
				res.BudgetExhausted = true
				break
			}
			if perTool[tc.Name] >= r.cfg.MaxPerTool {
				msgs = append(msgs, llm.ToolResultMessage(tc.ID,
					fmt.Sprintf("工具 %s 已调用 %d 次达到上限，请改用其他工具或提交结论。", tc.Name, perTool[tc.Name])))
				continue
			}
			if !r.cfg.Registry.Has(tc.Name) {
				msgs = append(msgs, llm.ToolResultMessage(tc.ID, fmt.Sprintf("不存在工具 %q", tc.Name)))
				continue
			}
			if chain.SeenCall(tc.Name, parseArgs(tc.Args)) {
				msgs = append(msgs, llm.ToolResultMessage(tc.ID,
					"相同参数已调用过，结果见前面的证据。请换参数或换工具。"))
				continue
			}

			// 无进展检测
			if chain.NoProgress() {
				res.BudgetExhausted = true
				break
			}

			perTool[tc.Name]++
			ev := r.executeTool(ctx, chain, tc)
			res.ToolCalls = countToolCalls(chain)
			msgs = append(msgs, llm.ToolResultMessage(tc.ID, renderToolResult(ev)))
		}

		if res.BudgetExhausted {
			break
		}
	}

	// 预算耗尽或轮数用尽：强制收尾
	res.BudgetExhausted = true
	r.progress("预算耗尽，请求模型基于已有证据收尾")
	final, err := r.forceConclusion(ctx, msgs, toolSpecs, chain)
	if err != nil {
		return res, err
	}
	res.Conclusion = final
	return res, nil
}

// executeTool 调用工具并写入证据链。
func (r *SessionRunner) executeTool(ctx context.Context, chain *Chain, tc llm.ToolCall) Evidence {
	args := parseArgs(tc.Args)
	ev := Evidence{Tool: tc.Name, Args: args, Round: chain.round}
	chain.MarkCall(tc.Name, args)

	out, raw, err := r.cfg.Registry.Execute(ctx, tc.Name, json.RawMessage(tc.Args))
	if raw != nil {
		ev.Node = raw.Node
		ev.Command = raw.Command
		ev.RawOutput = raw.Output
		ev.Duration = raw.Duration
		ev.ExitCode = raw.ExitCode
	}
	if err != nil {
		ev.ErrorKind = errorKind(err)
		ev.ErrorMsg = err.Error()
	} else if out != nil {
		ev.Summary = out.Summary
	}
	return chain.Add(ev)
}

// forceConclusion 请求模型基于已有证据给出结论，禁止继续调用工具。
func (r *SessionRunner) forceConclusion(ctx context.Context, msgs []llm.Message, specs []llm.ToolSpec, chain *Chain) (*Conclusion, error) {
	msgs = append(msgs, llm.Message{
		Role: llm.RoleUser,
		Content: "调查预算已耗尽。不要继续调用任何诊断工具。" +
			"请基于已收集的证据调用 " + ConcludeToolName + " 提交结论，内容需包括：" +
			"① 能确定的结论 ② 已排除的假设 ③ 还缺哪些关键证据以及如何获取。" +
			"如果证据不足以支持任何结论，将 severity 设为 low 并在 unknowns 中说明缺少什么。",
	})

	// 强制收尾也允许重试一次格式
	for attempt := 0; attempt < 3; attempt++ {
		reply, err := r.cfg.Model.Chat(ctx, msgs, []llm.ToolSpec{ConclusionToolSpec()})
		if err != nil {
			return nil, fmt.Errorf("强制收尾失败: %w", err)
		}
		if len(reply.ToolCalls) == 0 {
			if c, cerr := ParseConclusion(reply.Content); cerr == nil {
				return c, nil
			}
			msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: reply.Content})
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: "请调用 " + ConcludeToolName + " 并遵守 JSON 结构。"})
			continue
		}
		for _, tc := range reply.ToolCalls {
			if tc.Name != ConcludeToolName {
				msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{tc}})
				msgs = append(msgs, llm.ToolResultMessage(tc.ID, "预算已耗尽，不能调用诊断工具，请直接提交结论。"))
				continue
			}
			c, cerr := ParseConclusion(tc.Args)
			if cerr != nil {
				msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{tc}})
				msgs = append(msgs, llm.ToolResultMessage(tc.ID, "结论格式错误："+cerr.Error()))
				continue
			}
			if verr := chain.ValidateRefs(c.EvidenceRefs); verr != nil {
				msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{tc}})
				msgs = append(msgs, llm.ToolResultMessage(tc.ID, verr.Error()))
				continue
			}
			return c, nil
		}
	}
	return nil, fmt.Errorf("模型未能在预算耗尽后给出有效结论")
}

func (r *SessionRunner) progress(msg string) {
	if r.cfg.OnProgress != nil {
		r.cfg.OnProgress(msg)
	}
}

func toLLMSpecs(specs []tools.Spec) []llm.ToolSpec {
	out := make([]llm.ToolSpec, 0, len(specs))
	for _, s := range specs {
		out = append(out, llm.ToolSpec{Name: s.Name, Description: s.Description, Schema: s.Schema})
	}
	return out
}

func parseArgs(raw string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]any{"_raw": raw}
	}
	return m
}

// compressHistory 压缩较早的工具结果，避免长调查撑爆上下文。
//
// 规则：
//   - system / user / assistant 消息永不压缩
//   - 最近 keepRecent 条工具结果保持原样
//   - 更早的工具结果截断到 200 字符，但保留 evidence_id 以便追溯
//
// 截断而非摘要，因为摘要需要额外调用模型；工具结果本身已经是短 JSON。
func compressHistory(msgs []llm.Message, keepRecent int) []llm.Message {
	out := make([]llm.Message, len(msgs))
	copy(out, msgs)

	var toolIdx []int
	for i, m := range out {
		if m.Role == llm.RoleTool {
			toolIdx = append(toolIdx, i)
		}
	}
	cut := len(toolIdx) - keepRecent
	for n, i := range toolIdx {
		if n >= cut {
			break
		}
		if len(out[i].Content) > 200 {
			out[i].Content = keepEvidenceID(out[i].Content, 200)
		}
	}
	return out
}

// keepEvidenceID 截断内容但尽量保留 evidence_id 字段。
func keepEvidenceID(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	marker := "[已压缩，完整输出见证据链]"
	if idx := strings.Index(content, `"evidence_id":"`); idx >= 0 {
		rest := content[idx:]
		if end := strings.Index(rest[15:], `"`); end >= 0 {
			id := rest[:15+end+1]
			return id + `,"note":"` + marker + `"}`
		}
	}
	return content[:limit] + marker
}

func renderToolResult(ev Evidence) string {
	if ev.ErrorKind != "" {
		return fmt.Sprintf(`{"error_kind":%q,"message":%q,"evidence_id":%q}`,
			ev.ErrorKind, ev.ErrorMsg, ev.ID)
	}
	return fmt.Sprintf(`{"evidence_id":%q,"summary":%q}`, ev.ID, ev.Summary)
}

func errorKind(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "超时"):
		return "timeout"
	case strings.Contains(msg, "unreachable") || strings.Contains(msg, "不可达"):
		return "unreachable"
	case strings.Contains(msg, "unavailable") || strings.Contains(msg, "不可用") || strings.Contains(msg, "没有"):
		return "unavailable"
	}
	return "failed"
}

func countToolCalls(c *Chain) int {
	return c.Len()
}
```

- [ ] **Step 4: 补充 `Has` 方法**

`tools.Registry` 需要 `Has(name string) bool`。在 `pkg/doctor/tools/registry.go` 中新增：

```go
// Has 判断工具是否已注册。
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}
```

并加测试到 `registry_test.go`：

```go
func TestRegistryHas(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubTool{name: "x"})
	if !r.Has("x") {
		t.Error("expected Has(x) true")
	}
	if r.Has("y") {
		t.Error("expected Has(y) false")
	}
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./pkg/doctor/loop/ -v`
Expected: PASS

- [ ] **Step 6: 运行 race**

Run: `go test -race ./pkg/doctor/loop/`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add pkg/doctor/loop/loop.go pkg/doctor/loop/loop_test.go pkg/doctor/tools/registry.go pkg/doctor/tools/registry_test.go
git commit -m "feat(doctor/loop): add budgeted investigation loop with evidence-backed conclusions"
```

---
# M4：报告

### Task 19: Report 类型与证据回填

**Files:**
- Create: `pkg/doctor/report/report.go`
- Test: `pkg/doctor/report/report_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/report/report_test.go`：

```go
package report

import (
	"testing"
	"time"

	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
)

func newTestInput() Input {
	chain := loop.NewChain()
	chain.Add(loop.Evidence{
		Tool: "ceph_status", Node: "node-3",
		Command: "ceph status -f json", RawOutput: `{"health":"HEALTH_WARN"}`,
		Summary: "HEALTH_WARN，OSD 20/19", Duration: 2 * time.Second,
	})
	chain.Add(loop.Evidence{
		Tool: "host_io", Node: "node-3",
		Command: "iostat -x 1 3", RawOutput: "sdc 99.8%util",
		Summary: "node-3/sdc 利用率接近 100%", Duration: 4 * time.Second,
	})
	return Input{
		ClusterID: "fsid-abc",
		Health:    "HEALTH_WARN",
		StartedAt: time.Date(2026, 10, 23, 15, 32, 0, 0, time.UTC),
		Duration:  42 * time.Second,
		Chain:     chain,
		Conclusion: &loop.Conclusion{
			Severity:     "high",
			Headline:     "osd.7 所在盘 sdc 出现 I/O 错误",
			Impact:       "2/1440 PG 降级，无数据丢失风险",
			EvidenceRefs: []string{"ev-001", "ev-002"},
			RuledOut:     []loop.RuledOut{{Hypothesis: "网络问题", Ref: "ev-001"}},
			Recommendations: []loop.Recommendation{
				{Risk: "low", Action: "观察 15 分钟", Command: "ceph osd ok-to-stop 7"},
			},
			Unknowns: []loop.Unknown{{What: "SMART 状态", Why: "smartctl 不可用", How: "手工执行 smartctl -a /dev/sdc"}},
		},
		Rounds: 4, ToolCalls: 2,
	}
}

func TestBuildBackfillsEvidence(t *testing.T) {
	r, err := Build(newTestInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Evidence) != 2 {
		t.Fatalf("evidence = %d", len(r.Evidence))
	}
	// 命令与原始输出必须来自证据链，而不是模型
	if r.Evidence[0].Command != "ceph status -f json" {
		t.Errorf("command = %q", r.Evidence[0].Command)
	}
	if r.Evidence[1].RawOutput != "sdc 99.8%util" {
		t.Errorf("raw = %q", r.Evidence[1].RawOutput)
	}
}

func TestBuildRejectsMissingEvidenceRef(t *testing.T) {
	in := newTestInput()
	in.Conclusion.EvidenceRefs = []string{"ev-001", "ev-404"}
	if _, err := Build(in); err == nil {
		t.Fatal("expected error for missing evidence ref")
	}
}

func TestBuildAllowsNilConclusion(t *testing.T) {
	in := newTestInput()
	in.Conclusion = nil
	r, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Severity != "unknown" {
		t.Errorf("severity = %q, want unknown", r.Severity)
	}
	if len(r.Unknowns) == 0 {
		t.Error("nil conclusion should record why there is no conclusion")
	}
}

func TestBuildComputesStats(t *testing.T) {
	r, _ := Build(newTestInput())
	if r.Stats.Rounds != 4 || r.Stats.ToolCalls != 2 {
		t.Errorf("stats = %+v", r.Stats)
	}
	if r.Stats.EvidenceCount != 2 {
		t.Errorf("evidence count = %d", r.Stats.EvidenceCount)
	}
}

func TestBuildSeverityOrdering(t *testing.T) {
	if SeverityRank("critical") <= SeverityRank("high") {
		t.Error("critical should outrank high")
	}
	if SeverityRank("none") >= SeverityRank("low") {
		t.Error("none should rank below low")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/report/ -run TestBuild -v`
Expected: FAIL — `undefined: Input`

- [ ] **Step 3: 实现**

`pkg/doctor/report/report.go`：

```go
package report

import (
	"fmt"
	"time"

	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
)

// Input 是构建报告所需的全部材料。
type Input struct {
	ClusterID  string
	Health     string
	Capabilities string
	StartedAt  time.Time
	Duration   time.Duration
	Chain      *loop.Chain
	Conclusion *loop.Conclusion
	Rounds     int
	ToolCalls  int
	Notes      []string // 例如 host key 未校验的警示
}

// Report 是最终报告的数据模型。
type Report struct {
	ClusterID string        `json:"cluster_id"`
	Health    string        `json:"health"`
	Severity  string        `json:"severity"`
	Headline  string        `json:"headline"`
	Impact    string        `json:"impact"`
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration"`

	Evidence        []EvidenceView   `json:"evidence"`
	RuledOut        []loop.RuledOut  `json:"ruled_out"`
	Recommendations []loop.Recommendation `json:"recommendations"`
	Unknowns        []loop.Unknown   `json:"unknowns"`

	Capabilities string `json:"capabilities"` // 结论所基于的环境能力，如 host-ceph
	Stats        Stats  `json:"stats"`
	Notes        []string `json:"notes,omitempty"`
}

// EvidenceView 是证据链中可展示的字段。
type EvidenceView struct {
	ID        string        `json:"id"`
	Tool      string        `json:"tool"`
	Node      string        `json:"node,omitempty"`
	Command   string        `json:"command,omitempty"`
	Summary   string        `json:"summary,omitempty"`
	RawOutput string        `json:"raw_output,omitempty"`
	Duration  time.Duration `json:"duration"`
	ErrorKind string        `json:"error_kind,omitempty"`
	ErrorMsg  string        `json:"error_message,omitempty"`
}

// Stats 执行统计。
type Stats struct {
	Rounds        int `json:"rounds"`
	ToolCalls     int `json:"tool_calls"`
	EvidenceCount int `json:"evidence_count"`
}

// Build 由 Input 生成 Report，并从证据链回填证据。
func Build(in Input) (*Report, error) {
	r := &Report{
		ClusterID:    in.ClusterID,
		Health:       in.Health,
		Capabilities: in.Capabilities,
		StartedAt:    in.StartedAt,
		Duration:     in.Duration,
		Notes:        in.Notes,
		Stats: Stats{
			Rounds:    in.Rounds,
			ToolCalls: in.ToolCalls,
		},
	}

	if in.Chain != nil {
		for _, e := range in.Chain.All() {
			r.Evidence = append(r.Evidence, EvidenceView{
				ID: e.ID, Tool: e.Tool, Node: e.Node, Command: e.Command,
				Summary: e.Summary, RawOutput: e.RawOutput, Duration: e.Duration,
				ErrorKind: e.ErrorKind, ErrorMsg: e.ErrorMsg,
			})
		}
		r.Stats.EvidenceCount = len(r.Evidence)
	}

	if in.Conclusion == nil {
		r.Severity = "unknown"
		r.Headline = "未得出结论"
		r.Impact = "诊断未产出一致结论，请参考下方证据自行判断"
		r.Unknowns = []loop.Unknown{{
			What: "诊断结论",
			Why:  "模型未能在预算内提交有效结论",
			How:  "可查看证据列表手工分析，或提高调查预算后重跑",
		}}
		return r, nil
	}

	// 证据回填：引用的 ID 必须存在
	if in.Chain != nil {
		if err := in.Chain.ValidateRefs(in.Conclusion.EvidenceRefs); err != nil {
			return nil, fmt.Errorf("结论引用了不存在的证据: %w", err)
		}
		for _, ro := range in.Conclusion.RuledOut {
			if ro.Ref != "" {
				if err := in.Chain.ValidateRefs([]string{ro.Ref}); err != nil {
					return nil, fmt.Errorf("已排除项引用了不存在的证据: %w", err)
				}
			}
		}
	}

	r.Severity = in.Conclusion.Severity
	r.Headline = in.Conclusion.Headline
	r.Impact = in.Conclusion.Impact
	r.RuledOut = in.Conclusion.RuledOut
	r.Recommendations = in.Conclusion.Recommendations
	r.Unknowns = in.Conclusion.Unknowns
	return r, nil
}

// SeverityRank 数字越大越严重，用于渲染排序。
func SeverityRank(s string) int {
	switch s {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	case "none":
		return 0
	}
	return 0
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/doctor/report/ -run TestBuild -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/doctor/report/report.go pkg/doctor/report/report_test.go
git commit -m "feat(doctor/report): add report model with evidence backfill"
```

---

### Task 20: 完整文本与 brief 渲染

**Files:**
- Create: `pkg/doctor/report/render_text.go`
- Create: `pkg/doctor/report/render_brief.go`
- Test: `pkg/doctor/report/render_text_test.go`
- Test: `pkg/doctor/report/render_brief_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/report/render_text_test.go`：

```go
package report

import (
	"strings"
	"testing"
)

func TestRenderTextContainsAllSections(t *testing.T) {
	r, err := Build(newTestInput())
	if err != nil {
		t.Fatal(err)
	}
	out := RenderText(r)
	for _, section := range []string{"结论", "严重程度", "影响", "判断依据", "已排除", "建议操作", "未能确定"} {
		if !strings.Contains(out, section) {
			t.Errorf("missing section %q", section)
		}
	}
	if !strings.Contains(out, "fsid-abc") {
		t.Error("missing cluster id")
	}
	if !strings.Contains(out, "ceph status -f json") {
		t.Error("missing evidence command")
	}
	if !strings.Contains(out, "ev-001") {
		t.Error("missing evidence id")
	}
}

func TestRenderTextNeverEmitsEmptyUnknowns(t *testing.T) {
	in := newTestInput()
	in.Conclusion.Unknowns = []loop.Unknown{}
	r, _ := Build(in)
	out := RenderText(r)
	if !strings.Contains(out, "未能确定") {
		t.Fatal("unknowns section must always exist")
	}
	if !strings.Contains(out, "无") {
		t.Error("empty unknowns should render as 无")
	}
}

func TestRenderTextShowsRecommendationRisk(t *testing.T) {
	r, _ := Build(newTestInput())
	out := RenderText(r)
	if !strings.Contains(out, "低风险") {
		t.Error("expected risk label")
	}
	if !strings.Contains(out, "ceph osd ok-to-stop 7") {
		t.Error("expected command text")
	}
}

func TestRenderTextIncludesNotes(t *testing.T) {
	in := newTestInput()
	in.Notes = []string{"首节点 host key 未校验"}
	r, _ := Build(in)
	out := RenderText(r)
	if !strings.Contains(out, "host key 未校验") {
		t.Error("notes should appear in report")
	}
}
```

`render_text_test.go` 需 import `"github.com/microyahoo/infra-agent/pkg/doctor/loop"`。

`pkg/doctor/report/render_brief_test.go`：

```go
package report

import (
	"strings"
	"testing"
)

func TestRenderBriefIsShort(t *testing.T) {
	r, _ := Build(newTestInput())
	out := RenderBrief(r)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > 12 {
		t.Errorf("brief too long: %d lines\n%s", len(lines), out)
	}
	if !strings.Contains(out, "osd.7") {
		t.Error("brief should carry the headline")
	}
	if !strings.Contains(out, "HIGH") && !strings.Contains(out, "high") {
		t.Error("brief should carry severity")
	}
}

func TestRenderBriefOmitsEvidenceDump(t *testing.T) {
	r, _ := Build(newTestInput())
	out := RenderBrief(r)
	if strings.Contains(out, "sdc 99.8%util") {
		t.Error("brief should not dump raw outputs")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/report/ -run 'TestRender' -v`
Expected: FAIL — `undefined: RenderText`

- [ ] **Step 3: 实现 render_text.go**

`pkg/doctor/report/render_text.go`：

```go
package report

import (
	"fmt"
	"strings"
)

// RenderText 渲染完整的人读报告。
func RenderText(r *Report) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# 集群 %s · %s · %s\n\n", r.ClusterID, r.Health, r.StartedAt.Format("2006-01-02 15:04:05"))
	if r.Capabilities != "" {
		fmt.Fprintf(&b, "> 诊断能力后端：%s\n\n", r.Capabilities)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "> ⚠ %s\n\n", n)
	}

	b.WriteString("## 结论\n")
	fmt.Fprintf(&b, "%s\n\n", r.Headline)

	fmt.Fprintf(&b, "## 严重程度：%s\n", severityLabel(r.Severity))
	fmt.Fprintf(&b, "%s\n\n", r.Impact)

	b.WriteString("## 判断依据\n\n")
	if len(r.Evidence) == 0 {
		b.WriteString("（无采集到证据）\n\n")
	} else {
		b.WriteString("| 证据 | 查了什么 | 结果 |\n|---|---|---|\n")
		for _, e := range r.Evidence {
			result := e.Summary
			if e.ErrorKind != "" {
				result = fmt.Sprintf("失败（%s）：%s", e.ErrorKind, e.ErrorMsg)
			}
			fmt.Fprintf(&b, "| %s | `%s` | %s |\n", e.ID, e.Command, escapeCell(result))
		}
		b.WriteString("\n")
	}

	b.WriteString("## 已排除\n")
	if len(r.RuledOut) == 0 {
		b.WriteString("（无）\n\n")
	} else {
		for _, ro := range r.RuledOut {
			fmt.Fprintf(&b, "- %s [%s]\n", ro.Hypothesis, ro.Ref)
		}
		b.WriteString("\n")
	}

	b.WriteString("## 建议操作（不自动执行）\n")
	if len(r.Recommendations) == 0 {
		b.WriteString("（无）\n\n")
	} else {
		for i, rec := range r.Recommendations {
			fmt.Fprintf(&b, "%d. **[%s风险]** %s\n", i+1, riskLabel(rec.Risk), rec.Action)
			if rec.Command != "" {
				fmt.Fprintf(&b, "   ```\n   %s\n   ```\n", rec.Command)
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("## 未能确定\n")
	if len(r.Unknowns) == 0 {
		b.WriteString("无\n\n")
	} else {
		for _, u := range r.Unknowns {
			fmt.Fprintf(&b, "- **%s**：%s（获取方式：%s）\n", u.What, u.Why, u.How)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "---\n执行了 %d 次工具调用，%d 轮，耗时 %s · 完整命令记录见 --format json\n",
		r.Stats.ToolCalls, r.Stats.Rounds, r.Duration.Round(1e6))
	return b.String()
}

func severityLabel(s string) string {
	switch s {
	case "critical":
		return "Critical"
	case "high":
		return "High"
	case "medium":
		return "Medium"
	case "low":
		return "Low"
	case "none":
		return "无异常"
	}
	return "未知"
}

func riskLabel(r string) string {
	switch r {
	case "low":
		return "低"
	case "medium":
		return "中"
	case "high":
		return "高"
	}
	return r
}

func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
```

- [ ] **Step 4: 实现 render_brief.go**

`pkg/doctor/report/render_brief.go`：

```go
package report

import (
	"fmt"
	"strings"
)

// RenderBrief 渲染值班视角的极简报告。
func RenderBrief(r *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s · %s\n", strings.ToUpper(severityLabel(r.Severity)), r.ClusterID, r.Health)
	fmt.Fprintf(&b, "%s\n", r.Headline)
	if r.Impact != "" {
		fmt.Fprintf(&b, "影响：%s\n", r.Impact)
	}
	if len(r.Recommendations) > 0 {
		first := r.Recommendations[0]
		fmt.Fprintf(&b, "建议：%s", first.Action)
		if first.Command != "" {
			fmt.Fprintf(&b, "（`%s`）", first.Command)
		}
		b.WriteString("\n")
	}
	if len(r.Unknowns) > 0 {
		fmt.Fprintf(&b, "待确认：%s\n", r.Unknowns[0].What)
	}
	fmt.Fprintf(&b, "证据 %d 条 · %d 轮 · %s", r.Stats.EvidenceCount, r.Stats.Rounds, r.Duration.Round(1e6))
	return b.String()
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./pkg/doctor/report/ -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add pkg/doctor/report/render_text.go pkg/doctor/report/render_text_test.go \
        pkg/doctor/report/render_brief.go pkg/doctor/report/render_brief_test.go
git commit -m "feat(doctor/report): add full and brief text rendering"
```

---

### Task 21: JSON 渲染与多集群总览

**Files:**
- Create: `pkg/doctor/report/render_json.go`
- Test: `pkg/doctor/report/render_json_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/report/render_json_test.go`：

```go
package report

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderJSONIsValid(t *testing.T) {
	r, _ := Build(newTestInput())
	out, err := RenderJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if v["cluster_id"] != "fsid-abc" {
		t.Errorf("cluster_id = %v", v["cluster_id"])
	}
	if _, ok := v["evidence"]; !ok {
		t.Error("evidence missing")
	}
}

func TestRenderJSONEscapesRawOutputSafely(t *testing.T) {
	in := newTestInput()
	in.Chain.Add(loop.Evidence{Tool: "x", RawOutput: `output with "quotes" and \ backslash`})
	r, _ := Build(in)
	out, err := RenderJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
}

func TestRenderClusterOverview(t *testing.T) {
	rows := []OverviewRow{
		{ClusterID: "stor-01", Health: "HEALTH_WARN", Severity: "high", OSDs: "20/20", Capacity: "62%", Top: "osd.7 坏盘"},
		{ClusterID: "stor-02", Health: "HEALTH_OK", Severity: "none", OSDs: "24/24", Capacity: "48%", Top: "-"},
	}
	out := RenderOverview(rows)
	if !strings.Contains(out, "stor-01") || !strings.Contains(out, "stor-02") {
		t.Error("missing clusters")
	}
	if !strings.Contains(out, "集群") {
		t.Error("missing header")
	}
	if !strings.Contains(out, "62%") {
		t.Error("missing capacity")
	}
}
```

`render_json_test.go` 需 import `"github.com/microyahoo/infra-agent/pkg/doctor/loop"`。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/report/ -run TestRenderJSON -v`
Expected: FAIL — `undefined: RenderJSON`

- [ ] **Step 3: 实现**

`pkg/doctor/report/render_json.go`：

```go
package report

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RenderJSON 渲染机器可读报告。
func RenderJSON(r *Report) (string, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal report: %w", err)
	}
	return string(b), nil
}

// OverviewRow 多集群总览中的一行。
type OverviewRow struct {
	ClusterID string
	Health    string
	Severity  string
	OSDs      string
	Capacity  string
	Top       string
}

// RenderOverview 渲染多集群总览表。
func RenderOverview(rows []OverviewRow) string {
	var b strings.Builder
	b.WriteString("集群          健康         严重度   OSD     容量    主要问题\n")
	b.WriteString("------------- ------------- ------ ------- ------ -------------\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "%-13s %-13s %-6s %-7s %-6s %s\n",
			r.ClusterID, r.Health, severityLabel(r.Severity), r.OSDs, r.Capacity, r.Top)
	}
	return b.String()
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/doctor/report/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add pkg/doctor/report/render_json.go pkg/doctor/report/render_json_test.go
git commit -m "feat(doctor/report): add JSON rendering and multi-cluster overview"
```

---
# M5：编排与 CLI

### Task 22: doctor 编排入口

**Files:**
- Create: `pkg/doctor/doctor.go`
- Create: `pkg/doctor/prompt.go`
- Test: `pkg/doctor/doctor_test.go`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/doctor_test.go`：

```go
package doctor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
	"github.com/microyahoo/infra-agent/pkg/doctor/llm"
	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
	"github.com/microyahoo/infra-agent/pkg/doctor/report"
)

// fakeCluster 实现 Cluster 接口。
type fakeCluster struct {
	caps    access.Capabilities
	topo    *access.Topology
	outputs map[string]access.Output
}

func (f *fakeCluster) Run(_ context.Context, node string, argv []string) (access.Output, error) {
	key := node + "|" + strings.Join(argv, " ")
	if out, ok := f.outputs[key]; ok {
		return out, nil
	}
	return access.Output{}, &access.ToolError{Kind: access.KindFailed, Message: "not stubbed"}
}
func (f *fakeCluster) Capabilities(_ context.Context, _ string) access.Capabilities { return f.caps }
func (f *fakeCluster) Topology(_ context.Context) (*access.Topology, error)         { return f.topo, nil }
func (f *fakeCluster) Nodes() []string                                               { return []string{"node-3"} }
func (f *fakeCluster) Close() error                                                  { return nil }

type fakeModel struct{ reply llm.Reply }

func (m *fakeModel) Chat(_ context.Context, msgs []llm.Message, _ []llm.ToolSpec) (llm.Reply, error) {
	return m.reply, nil
}

func newFakeCluster() *fakeCluster {
	return &fakeCluster{
		caps: access.Capabilities{
			CephCLI: true, CephVersion: "18.2.4", Backend: "host-ceph",
			LogDirs: []string{"/var/log/ceph"},
			HostTools: map[string]bool{"iostat": true, "smartctl": true},
		},
		topo: &access.Topology{
			FSID: "fsid-abc",
			Nodes: []access.TopoNode{{Name: "node-3", Host: "node-3", OSDIDs: []int{7}}},
			OSDs:  []access.OSDPlacement{{ID: 7, Host: "node-3", Device: "sdc", Status: "up"}},
		},
		outputs: map[string]access.Output{
			"node-3|ceph status -f json": {Stdout: `{"fsid":"fsid-abc","health":{"status":"HEALTH_WARN"},"monmap":{"num_mons":3},"osdmap":{"num_osds":1,"num_up_osds":1,"num_in_osds":1},"pgmap":{"num_pgs":10,"pgs_by_state":[{"state_name":"active+clean","count":10}],"bytes_total":100,"bytes_used":50}}`},
			"node-3|ceph health detail":  {Stdout: "HEALTH_WARN"},
		},
	}
}

func TestDoctorBuildsReport(t *testing.T) {
	concl, _ := json.Marshal(map[string]any{
		"severity": "low", "headline": "一切正常", "impact": "无",
		"evidence_refs": []string{}, "ruled_out": []any{},
		"recommendations": []any{}, "unknowns": []any{},
	})
	d := New(Config{
		Cluster: newFakeCluster(),
		Model:   &fakeModel{reply: llm.Reply{ToolCalls: []llm.ToolCall{{ID: "c1", Name: loop.ConcludeToolName, Args: string(concl)}}}},
		SourceDir: "",
	})
	rep, err := d.Run(context.Background(), Task{Question: "体检"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Headline != "一切正常" {
		t.Errorf("headline = %q", rep.Headline)
	}
	if rep.ClusterID != "fsid-abc" {
		t.Errorf("cluster id = %q", rep.ClusterID)
	}
	if len(rep.Evidence) == 0 {
		t.Error("expected snapshot evidence")
	}
}

func TestDoctorRendersThreeFormats(t *testing.T) {
	concl, _ := json.Marshal(map[string]any{
		"severity": "high", "headline": "盘有问题", "impact": "少量降级",
		"evidence_refs": []string{}, "ruled_out": []any{},
		"recommendations": []any{map[string]any{"risk": "low", "action": "观察"}},
		"unknowns":        []any{},
	})
	d := New(Config{
		Cluster: newFakeCluster(),
		Model:   &fakeModel{reply: llm.Reply{ToolCalls: []llm.ToolCall{{ID: "c1", Name: loop.ConcludeToolName, Args: string(concl)}}}},
	})
	rep, err := d.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}

	text, err := Render(rep, FormatText)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "盘有问题") {
		t.Error("text missing headline")
	}

	brief, err := Render(rep, FormatBrief)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief, "盘有问题") {
		t.Error("brief missing headline")
	}

	js, err := Render(rep, FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(js)) {
		t.Error("invalid json")
	}
}

func TestDoctorHandlesUnavailableCLI(t *testing.T) {
	fc := newFakeCluster()
	fc.caps.CephCLI = false
	fc.caps.Backend = "log-only"
	concl, _ := json.Marshal(map[string]any{
		"severity": "low", "headline": "只能看日志", "impact": "未知",
		"evidence_refs": []string{}, "ruled_out": []any{},
		"recommendations": []any{}, "unknowns": []any{},
	})
	d := New(Config{
		Cluster: fc,
		Model:   &fakeModel{reply: llm.Reply{ToolCalls: []llm.ToolCall{{ID: "c1", Name: loop.ConcludeToolName, Args: string(concl)}}}},
	})
	rep, err := d.Run(context.Background(), Task{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if rep == nil {
		t.Fatal("expected report even without ceph CLI")
	}
}

func TestSnapshotSystemPromptContainsCapabilities(t *testing.T) {
	fc := newFakeCluster()
	snap := Snapshot{
		ClusterID:    "fsid-abc",
		Health:       "HEALTH_WARN",
		Capabilities: fc.caps,
		Topology:     fc.topo,
	}
	p := SystemPrompt(snap, "体检一下")
	if !strings.Contains(p, "18.2.4") {
		t.Error("prompt should include ceph version")
	}
	if !strings.Contains(p, "host-ceph") {
		t.Error("prompt should include backend")
	}
	if !strings.Contains(p, loop.ConcludeToolName) {
		t.Error("prompt should mention conclusion tool")
	}
	if !strings.Contains(p, "只读") && !strings.Contains(p, "不能变更") {
		t.Error("prompt should state read-only constraint")
	}
}

func TestRenderRejectsUnknownFormat(t *testing.T) {
	rep, _ := report.Build(report.Input{})
	if _, err := Render(rep, "xml"); err == nil {
		t.Fatal("expected error for unknown format")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/ -v`
Expected: FAIL — `undefined: New`

- [ ] **Step 3: 实现 prompt.go**

`pkg/doctor/prompt.go`：

```go
package doctor

import (
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
)

// Snapshot 是调查开始前的确定性采集结果。
type Snapshot struct {
	ClusterID    string
	Health       string
	Summary      string
	Capabilities access.Capabilities
	Topology     *access.Topology
	Failures     []string
}

// SystemPrompt 组装系统提示词。
func SystemPrompt(s Snapshot, question string) string {
	var b strings.Builder

	b.WriteString(`你是一名 Ceph 存储集群诊断专家。你的任务是调查集群当前状态并给出结论。

## 硬性规则
1. 你只能查询，不能变更集群。所有工具都是只读的，不要试图提议工具之外的变更操作。
2. 结论中的每一个判断都必须有证据支撑。结论必须通过 ` + loop.ConcludeToolName + ` 工具提交。
3. evidence_refs 只能引用真实返回过的 evidence_id，不能编造。
4. 证据不足时不要猜测。在 unknowns 中写清楚缺少什么、为什么缺、怎么获取。
5. 同一个工具用相同参数不要重复调用，结果已经在对话里了。
6. 优先用 ceph_status / osd_tree 等全景工具建立整体判断，再针对异常点深挖。工具的 hints 字段会提示下一步可查什么。

`)

	b.WriteString("## 集群快照
")
	fmt.Fprintf(&b, "- 集群 FSID：%s
", orDash(s.ClusterID))
	fmt.Fprintf(&b, "- 健康状态：%s
", orDash(s.Health))
	if s.Summary != "" {
		fmt.Fprintf(&b, "- 摘要：%s
", s.Summary)
	}

	b.WriteString("
## 可用能力
")
	fmt.Fprintf(&b, "- ceph CLI：%v（后端 %s，版本 %s）
", s.Capabilities.CephCLI, orDash(s.Capabilities.Backend), orDash(s.Capabilities.CephVersion))
	fmt.Fprintf(&b, "- kubectl：%v
", s.Capabilities.Kubectl)
	fmt.Fprintf(&b, "- 容器运行时：%s
", orDash(s.Capabilities.ContainerRT))
	fmt.Fprintf(&b, "- 日志目录：%s
", orDash(strings.Join(s.Capabilities.LogDirs, ", ")))
	if len(s.Capabilities.HostTools) > 0 {
		var tools []string
		for name, ok := range s.Capabilities.HostTools {
			if ok {
				tools = append(tools, name)
			}
		}
		fmt.Fprintf(&b, "- 宿主机工具：%s
", orDash(strings.Join(tools, ", ")))
	}

	if s.Topology != nil && len(s.Topology.Nodes) > 0 {
		b.WriteString("
## 节点与 OSD
")
		for _, n := range s.Topology.Nodes {
			fmt.Fprintf(&b, "- %s：OSD %v
", n.Name, n.OSDIDs)
		}
	}

	if len(s.Failures) > 0 {
		b.WriteString("
## 快照阶段的失败项
")
		for _, f := range s.Failures {
			fmt.Fprintf(&b, "- %s
", f)
		}
	}

	b.WriteString("
## 本次任务
")
	b.WriteString(question)
	b.WriteString("
")
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
```

- [ ] **Step 4: 实现 doctor.go**

`pkg/doctor/doctor.go`：

```go
package doctor

import (
	"context"
	"fmt"
	"time"

	"github.com/microyahoo/infra-agent/pkg/doctor/access"
	"github.com/microyahoo/infra-agent/pkg/doctor/llm"
	"github.com/microyahoo/infra-agent/pkg/doctor/loop"
	"github.com/microyahoo/infra-agent/pkg/doctor/report"
	"github.com/microyahoo/infra-agent/pkg/doctor/tools"
)

// Cluster 是编排对接入层的依赖，便于测试替换。
type Cluster interface {
	Run(ctx context.Context, node string, argv []string) (access.Output, error)
	Capabilities(ctx context.Context, node string) access.Capabilities
	Topology(ctx context.Context) (*access.Topology, error)
	Nodes() []string
	Close() error
}

// Config 编排配置。
type Config struct {
	Cluster      Cluster
	Model        llm.ChatModel
	SourceDir    string
	MaxRounds    int
	MaxToolCalls int
	OnProgress   func(string)
}

// Doctor 编排一次完整诊断。
type Doctor struct {
	cfg Config
}

func New(cfg Config) *Doctor { return &Doctor{cfg: cfg} }

// Task 一次诊断任务。
type Task struct {
	Question string
}

// Report 是报告的别名，方便调用方。
type Report = report.Report

// Format 输出格式。
type Format string

const (
	FormatText  Format = "text"
	FormatBrief Format = "brief"
	FormatJSON  Format = "json"
)

// Run 执行：快照 → 循环 → 报告。
func (d *Doctor) Run(ctx context.Context, task Task) (*Report, error) {
	start := time.Now()

	runner := &clusterRunner{cluster: d.cfg.Cluster}
	reg := tools.RegistryFrom(tools.All(runner, d.cfg.SourceDir))

	snapshot := d.snapshot(ctx, runner)
	question := task.Question
	if question == "" {
		question = "请全面检查这个集群的健康状况，指出存在的问题并给出处理建议。"
	}

	lr := loop.NewRunner(loop.RunnerConfig{
		Model:        d.cfg.Model,
		Registry:     reg,
		SystemPrompt: SystemPrompt(snapshot, question),
		MaxRounds:    d.cfg.MaxRounds,
		MaxToolCalls: d.cfg.MaxToolCalls,
		OnProgress:   d.cfg.OnProgress,
	})

	res, err := lr.Run(ctx, loop.Task{ClusterID: snapshot.ClusterID, Question: question})
	if err != nil {
		return nil, err
	}

	notes := []string{}
	if !nodesReachable(runner) {
		notes = append(notes, "部分节点不可达，结论可能不完整")
	}

	return report.Build(report.Input{
		ClusterID:    snapshot.ClusterID,
		Health:       snapshot.Health,
		Capabilities: snapshot.Capabilities.Backend,
		StartedAt:    start,
		Duration:     time.Since(start),
		Chain:        res.Chain,
		Conclusion:   res.Conclusion,
		Rounds:       res.Rounds,
		ToolCalls:    res.ToolCalls,
		Notes:        notes,
	})
}

// Render 按格式渲染。
func Render(r *Report, f Format) (string, error) {
	switch f {
	case FormatText, "":
		return report.RenderText(r), nil
	case FormatBrief:
		return report.RenderBrief(r), nil
	case FormatJSON:
		return report.RenderJSON(r)
	}
	return "", fmt.Errorf("不支持的输出格式 %q（可选 text/brief/json）", f)
}

// snapshot 采集确定性快照。
func (d *Doctor) snapshot(ctx context.Context, r *clusterRunner) Snapshot {
	primary := primaryOf(r)
	caps := r.Capabilities(ctx, primary)
	s := Snapshot{Capabilities: caps}

	topo, terr := r.Topology(ctx)
	if terr != nil {
		s.Failures = append(s.Failures, "拓扑发现失败："+terr.Error())
	} else {
		s.Topology = topo
		if topo != nil {
			s.ClusterID = topo.FSID
		}
	}

	if !caps.CephCLI {
		s.Health = "unknown"
		s.Summary = "无 ceph CLI，仅能通过日志分析"
		if len(caps.LogDirs) == 0 {
			s.Failures = append(s.Failures, "未找到任何日志目录，可用的诊断手段非常有限")
		}
		return s
	}

	out, err := r.Run(ctx, primary, []string{"ceph", "status", "-f", "json"})
	if err != nil {
		s.Failures = append(s.Failures, "ceph status 失败："+err.Error())
		s.Health = "unknown"
		return s
	}
	s.Health, s.Summary = summarizeStatus(out.Stdout)
	return s
}

// summarizeStatus 从状态 JSON 里抽一行摘要，避免快照阶段依赖工具层。
func summarizeStatus(raw string) (health, summary string) {
	health = extractString(raw, `"status":"`)
	summary = "已采集 ceph status，详情可调用 ceph_status 获取"
	if health == "" {
		health = "unknown"
	}
	return health, summary
}

func extractString(raw, prefix string) string {
	idx := indexOf(raw, prefix)
	if idx < 0 {
		return ""
	}
	rest := raw[idx+len(prefix):]
	end := indexOf(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func primaryOf(r *clusterRunner) string {
	n := r.Nodes()
	if len(n) > 0 {
		return n[0]
	}
	return ""
}

func nodesReachable(r *clusterRunner) bool {
	return len(r.Nodes()) > 0
}

// clusterRunner 把 Cluster 适配成 tools.Runner。
type clusterRunner struct {
	cluster Cluster
}

func (c *clusterRunner) Run(ctx context.Context, node string, argv []string) (access.Output, error) {
	return c.cluster.Run(ctx, node, argv)
}
func (c *clusterRunner) Capabilities(ctx context.Context, node string) access.Capabilities {
	return c.cluster.Capabilities(ctx, node)
}
func (c *clusterRunner) Topology(ctx context.Context) (*access.Topology, error) {
	return c.cluster.Topology(ctx)
}
func (c *clusterRunner) Nodes() []string { return c.cluster.Nodes() }
```

- [ ] **Step 5: 补 tools.All**

在 `pkg/doctor/tools/registry.go` 增加：

```go
// All 返回全部工具实例（用于需要注入源码目录等参数的场景）。
func All(r Runner, srcDir string) []Tool {
	return []Tool{
		NewCephStatus(r),
		NewOSDTree(r),
		NewPoolInfo(r),
		NewPGDetail(r),
		NewOSDDetail(r),
		NewDaemonLog(r),
		NewSlowOps(r),
		NewPerfCounters(r),
		NewHostIO(r),
		NewReadLog(r),
		NewHostCheck(r),
		NewSourceLookup(r, srcDir),
	}
}
```

并让 `DefaultRegistry` 复用 `All`：

```go
func DefaultRegistry(r Runner) *Registry {
	return RegistryFrom(All(r, ""))
}

// RegistryFrom 由工具列表构建注册表。
func RegistryFrom(list []Tool) *Registry {
	reg := NewRegistry()
	for _, t := range list {
		if err := reg.Register(t); err != nil {
			panic(err)
		}
	}
	return reg
}
```

- [ ] **Step 6: 运行测试确认通过**

Run: `go test ./pkg/doctor/ -v`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add pkg/doctor/doctor.go pkg/doctor/prompt.go pkg/doctor/doctor_test.go pkg/doctor/tools/registry.go
git commit -m "feat(doctor): add orchestration entry with snapshot and prompt assembly"
```

---

### Task 23: CLI 命令

**Files:**
- Create: `cmd/doctor.go`
- Modify: `cmd/root.go`（无改动，cobra 自动注册）

- [ ] **Step 1: 实现 cmd/doctor.go**

```go
package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/microyahoo/infra-agent/pkg/doctor"
	"github.com/microyahoo/infra-agent/pkg/doctor/access"
	"github.com/microyahoo/infra-agent/pkg/doctor/llm"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	doctorHost       string
	doctorPort       int
	doctorUser       string
	doctorKey        string
	doctorPassword   string
	doctorKnownHosts string
	doctorSourceDir  string
	doctorFormat     string
	doctorQuestion   string
	doctorTimeout    int
	doctorMaxRounds  int
	doctorModel      string
	doctorBaseURL    string
	doctorAPIKeyEnv  string
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "登录 Ceph 集群节点进行自主诊断",
	Long: `通过 SSH 登录到 Ceph 集群的首个节点，自动探测集群能力、发现拓扑，
然后由 AI 自主执行只读诊断命令，最终给出带证据的诊断报告。

全程只读：所有命令经过白名单校验，不会执行任何变更操作。

示例:
  infra-agent doctor --host 10.20.30.1 --user root --key ~/.ssh/id_rsa
  infra-agent doctor --host 10.20.30.1 --format brief
  infra-agent doctor --host 10.20.30.1 --question "最近写入延迟很高，帮我查一下"`,
	RunE: runDoctor,
}

func init() {
	rootCmd.AddCommand(doctorCmd)

	doctorCmd.Flags().StringVar(&doctorHost, "host", "", "首节点 IP（必填）")
	doctorCmd.Flags().IntVar(&doctorPort, "port", 22, "SSH 端口")
	doctorCmd.Flags().StringVar(&doctorUser, "user", "root", "SSH 用户")
	doctorCmd.Flags().StringVar(&doctorKey, "key", "", "SSH 私钥路径")
	doctorCmd.Flags().StringVar(&doctorPassword, "password", "", "SSH 密码（不推荐，优先用私钥）")
	doctorCmd.Flags().StringVar(&doctorKnownHosts, "known-hosts", "", "known_hosts 路径，留空则不校验 host key")
	doctorCmd.Flags().StringVar(&doctorSourceDir, "ceph-src", "", "Ceph 源码目录，用于日志反查源码")
	doctorCmd.Flags().StringVar(&doctorFormat, "format", "text", "输出格式：text / brief / json")
	doctorCmd.Flags().StringVar(&doctorQuestion, "question", "", "自定义诊断问题，留空则做全面体检")
	doctorCmd.Flags().IntVar(&doctorTimeout, "timeout", 600, "总诊断超时（秒）")
	doctorCmd.Flags().IntVar(&doctorMaxRounds, "max-rounds", 20, "最大调查轮数")
	doctorCmd.Flags().StringVar(&doctorModel, "model", "", "模型名，默认取配置")
	doctorCmd.Flags().StringVar(&doctorBaseURL, "base-url", "", "OpenAI 兼容接口地址")
	doctorCmd.Flags().StringVar(&doctorAPIKeyEnv, "api-key-env", "OPENAI_API_KEY", "提供 API Key 的环境变量名")

	doctorCmd.MarkFlagRequired("host")
}

func runDoctor(cmd *cobra.Command, args []string) error {
	if doctorKey == "" && doctorPassword == "" {
		return fmt.Errorf("必须提供 --key 或 --password")
	}

	apiKey := os.Getenv(doctorAPIKeyEnv)
	if apiKey == "" {
		return fmt.Errorf("环境变量 %s 为空，请先设置 API Key", doctorAPIKeyEnv)
	}

	model := doctorModel
	baseURL := doctorBaseURL
	if model == "" {
		model = viper.GetString("doctor.llm.model")
	}
	if baseURL == "" {
		baseURL = viper.GetString("doctor.llm.base_url")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(doctorTimeout)*time.Second)
	defer cancel()

	fmt.Fprintf(os.Stderr, "🔌 连接 %s ...
", doctorHost)
	cluster, err := access.Dial(ctx, access.Config{
		Host:           doctorHost,
		Port:           doctorPort,
		User:           doctorUser,
		PrivateKeyPath: doctorKey,
		Password:       doctorPassword,
		KnownHostsPath: doctorKnownHosts,
	})
	if err != nil {
		return fmt.Errorf("接入失败: %w", err)
	}
	defer cluster.Close()

	d := doctor.New(doctor.Config{
		Cluster:      cluster,
		Model:        llm.NewClient(llm.Config{BaseURL: baseURL, APIKey: apiKey, Model: model}),
		SourceDir:    doctorSourceDir,
		MaxRounds:    doctorMaxRounds,
		OnProgress:   func(msg string) { fmt.Fprintf(os.Stderr, "  · %s
", msg) },
	})

	rep, err := d.Run(ctx, doctor.Task{Question: doctorQuestion})
	if err != nil {
		return err
	}

	out, err := doctor.Render(rep, doctor.Format(doctorFormat))
	if err != nil {
		return err
	}
	fmt.Println(out)

	if rep.Severity == "critical" || rep.Severity == "high" {
		os.Exit(2)
	}
	return nil
}
```

- [ ] **Step 2: 构建验证**

Run: `go build ./... && go vet ./pkg/doctor/...`
Expected: 无错误

- [ ] **Step 3: 验证 CLI 帮助**

Run: `go run . doctor --help`
Expected: 显示所有参数

- [ ] **Step 4: 提交**

```bash
git add cmd/doctor.go
git commit -m "feat(cmd): add doctor command for autonomous Ceph diagnosis"
```

---

# M6：基线与回归

### Task 24: 录制基线与回放回归

**Files:**
- Create: `pkg/doctor/access/recorder.go`
- Create: `pkg/doctor/access/replay.go`
- Create: `pkg/doctor/access/recorder_test.go`
- Create: `testdata/replay/README.md`

- [ ] **Step 1: 写失败测试**

`pkg/doctor/access/recorder_test.go`：

```go
package access

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecorderWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec.Record("node-3", []string{"ceph", "-s"}, Output{Stdout: "HEALTH_OK", ExitCode: 0})
	rec.Record("node-4", []string{"ceph", "df"}, Output{Stdout: "data"})
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "
")
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	var rec1 ReplayEntry
	if err := json.Unmarshal([]byte(lines[0]), &rec1); err != nil {
		t.Fatal(err)
	}
	if rec1.Node != "node-3" || rec1.Stdout != "HEALTH_OK" {
		t.Errorf("entry = %+v", rec1)
	}
}

func TestReplayReturnsRecordedOutput(t *testing.T) {
	dir := t.TempDir()
	rec, _ := NewRecorder(dir)
	rec.Record("node-3", []string{"ceph", "-s"}, Output{Stdout: "HEALTH_OK"})
	rec.Close()

	rp, err := NewReplay(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := rp.Run(context.Background(), "node-3", []string{"ceph", "-s"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Stdout != "HEALTH_OK" {
		t.Errorf("stdout = %q", out.Stdout)
	}
}

func TestReplayFailsOnUnrecordedCall(t *testing.T) {
	dir := t.TempDir()
	rec, _ := NewRecorder(dir)
	rec.Record("node-3", []string{"ceph", "-s"}, Output{Stdout: "x"})
	rec.Close()

	rp, _ := NewReplay(dir)
	_, err := rp.Run(context.Background(), "node-3", []string{"ceph", "df"})
	if err == nil {
		t.Fatal("expected error for unrecorded call")
	}
	if !strings.Contains(err.Error(), "未录制") {
		t.Errorf("error = %v", err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/doctor/access/ -run 'TestRecorder|TestReplay' -v`
Expected: FAIL — `undefined: NewRecorder`

- [ ] **Step 3: 实现 recorder.go**

`pkg/doctor/access/recorder.go`：

```go
package access

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ReplayEntry 是一条录制记录。
type ReplayEntry struct {
	Node     string        `json:"node"`
	Argv     []string      `json:"argv"`
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
}

// Recorder 把真实执行结果写成 JSONL，供离线回放。
type Recorder struct {
	mu   sync.Mutex
	f    *os.File
	w    *bufio.Writer
	path string
}

func NewRecorder(dir string) (*Recorder, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create replay dir: %w", err)
	}
	p := filepath.Join(dir, "session.jsonl")
	f, err := os.Create(p)
	if err != nil {
		return nil, fmt.Errorf("create replay file: %w", err)
	}
	return &Recorder{f: f, w: bufio.NewWriter(f), path: p}, nil
}

// Record 写入一条记录。
func (r *Recorder) Record(node string, argv []string, out Output) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := ReplayEntry{
		Node: node, Argv: argv, Stdout: out.Stdout, Stderr: out.Stderr,
		ExitCode: out.ExitCode, Duration: out.Duration,
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	r.w.Write(b)
	r.w.WriteString("
")
}

// Close 落盘。
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.w.Flush(); err != nil {
		return err
	}
	return r.f.Close()
}

// Path 返回录制文件路径。
func (r *Recorder) Path() string { return r.path }
```

- [ ] **Step 4: 实现 replay.go**

`pkg/doctor/access/replay.go`：

```go
package access

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Replay 从录制文件回放命令结果，使测试不依赖真实集群。
type Replay struct {
	entries map[string]ReplayEntry
}

func replayKey(node string, argv []string) string {
	return node + "|" + strings.Join(argv, " ")
}

func NewReplay(dir string) (*Replay, error) {
	p := filepath.Join(dir, "session.jsonl")
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open replay: %w", err)
	}
	defer f.Close()

	rp := &Replay{entries: map[string]ReplayEntry{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e ReplayEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("parse replay line: %w", err)
		}
		rp.entries[replayKey(e.Node, e.Argv)] = e
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return rp, nil
}

// Run 回放一条命令。
func (r *Replay) Run(_ context.Context, node string, argv []string) (Output, error) {
	e, ok := r.entries[replayKey(node, argv)]
	if !ok {
		return Output{}, fmt.Errorf("回放中未录制该调用: %s %v", node, argv)
	}
	if e.Error != "" {
		return Output{Node: node, Command: BuildCommand(argv), Stdout: e.Stdout, Stderr: e.Stderr, ExitCode: e.ExitCode, Duration: e.Duration},
			&ToolError{Kind: KindFailed, Message: e.Error}
	}
	return Output{Node: node, Command: BuildCommand(argv), Stdout: e.Stdout, Stderr: e.Stderr, ExitCode: e.ExitCode, Duration: e.Duration}, nil
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./pkg/doctor/access/ -run 'TestRecorder|TestReplay' -v`
Expected: PASS

- [ ] **Step 6: 写 testdata 说明**

`testdata/replay/README.md`：

```markdown
# 回放基线

本目录存放从真实集群录制的会话，用于离线回归。

## 目录结构

```
testdata/replay/
  <cluster-id>/
    session.jsonl        每行一条 {node, argv, stdout, stderr, exit_code, duration}
    meta.json            {cluster_id, ceph_version, recorded_at, note}
```

## 录制方式

```bash
# 在 access 层开启录制后跑一次真实诊断
go run . doctor --host <首节点> --record testdata/replay/<cluster-id>
```

## 用途

- 开发与 CI 不依赖真实集群
- 40 套集群各录一份，作为改动后的回归基线
- 排查 agent 行为问题时可直接回放复现

## 注意

录制内容含真实集群输出，提交前请确认无敏感信息（IP、凭据）。
```

- [ ] **Step 7: 提交**

```bash
git add pkg/doctor/access/recorder.go pkg/doctor/access/replay.go \
        pkg/doctor/access/recorder_test.go testdata/replay/README.md
git commit -m "feat(doctor/access): add session recorder and replay for offline testing"
```

---

## 验收清单

全部任务完成后，逐项确认：

- [ ] `go build ./...` 通过
- [ ] `go test -race ./pkg/doctor/...` 全部通过
- [ ] `go vet ./pkg/doctor/...` 无告警
- [ ] `infra-agent doctor --help` 显示全部参数
- [ ] 对真实集群执行 `infra-agent doctor --host <ip> --key <key>`，产出带证据的报告
- [ ] 尝试构造写操作（如 `ceph osd out 7`）被策略拒绝
- [ ] `--format brief` 输出不超过 12 行
- [ ] `--format json` 输出可被 `jq` 解析
- [ ] 证据链中每条引用都能在报告中找到对应命令与输出

## 已知限制

| 限制 | 原因 | 后续 |
|---|---|---|
| 不支持多集群并发 | 第一版聚焦单集群 | 第二阶段加调度器 |
| 不接 Prometheus | 先验证命令行证据链 | 第二阶段 |
| 不做自动修复 | 安全边界 | 不计划 |
| 源码反查依赖本地目录 | 内网环境 | 可扩展为 HTTP 服务 |