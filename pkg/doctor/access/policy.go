package access

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Policy 只读策略。在命令真正执行前校验 argv。
// 这是「硬只读」的执行点：工具层即使写错，也无法在此通过。
//
// 两种模式：
//   - deny != nil：禁止变更式（默认）。拒绝变更命令，其余只读放行，
//     使模型能自主决定查什么，而不是被锁在预设的命令清单里。
//   - deny == nil：白名单式（旧行为，保留用于对照与严格场景）。
type Policy struct {
	deny *denyPolicy // 非空时使用禁止变更式校验

	binaryAllow map[string]bool
	cephVerbs   map[string]bool // "osd tree" 这类两级子命令
	cephDeny    map[string]bool // 显式的变更动词
	pathPrefix  []string        // 允许读取的路径前缀
}

// DefaultPolicy 返回默认只读策略：禁止变更式。
//
// 之所以默认用禁止清单而不是白名单：白名单把诊断能力锁在作者预设的场景里，
// 遇到没预设的故障模型无手段可查。禁止清单保留了"不能变更"这个硬约束，
// 同时让模型能自主决定查什么。
func DefaultPolicy() *Policy {
	return DefaultDenyPolicy()
}

// legacyAllowlistPolicy 是旧的白名单实现，保留用于严格场景与对照测试。
func legacyAllowlistPolicy() *Policy {
	return &Policy{
		binaryAllow: map[string]bool{
			"ceph": true, "crictl": true, "docker": true, "kubectl": true,
			"iostat": true, "smartctl": true, "dmesg": true, "df": true,
			"free": true, "uptime": true, "lsblk": true, "tail": true, "grep": true,
			"ls": true, "command": true, "nproc": true,
		},
		cephVerbs: map[string]bool{
			"-s": true, "--version": true, "-v": true,
			"status": true, "health": true, "df": true, "versions": true,
			"log": true, "tell": true, "daemon": true, "fsid": true,
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
		// /var/lib/rook 覆盖任意命名空间下的 Rook 日志目录（/var/lib/rook/<ns>/log）。
		pathPrefix: []string{"/var/log/ceph", "/var/lib/rook"},
	}
}

// Check 校验一条 argv 是否允许执行。
func (p *Policy) Check(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("rejected: empty argv")
	}
	if p.deny != nil {
		return p.checkDeny(argv)
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

func (p *Policy) checkCeph(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("rejected: bare ceph")
	}
	// tell 的形态是 tell <daemon> <cmd>...，只允许只读 cmd。
	// 必须按「完整子命令形状」匹配，只比对 args[2] 会漏掉
	// tell osd.N perf reset 这类「首词只读、后续参数才变更」的命令。
	if args[0] == "tell" {
		return p.checkCephTell(args)
	}
	// pg <pgid> query 形态
	if args[0] == "pg" && len(args) >= 3 && args[2] == "query" {
		return nil
	}
	// 先试两级（同时试三级，覆盖 "osd pool autoscale-status" 这类形态）
	if len(args) >= 2 {
		two := args[0] + " " + args[1]
		if p.cephDeny[two] {
			return fmt.Errorf("rejected: ceph %q is a mutating command", two)
		}
		if p.cephVerbs[two] {
			return nil
		}
	}
	if len(args) >= 3 {
		three := args[0] + " " + args[1] + " " + args[2]
		if p.cephDeny[three] {
			return fmt.Errorf("rejected: ceph %q is a mutating command", three)
		}
		if p.cephVerbs[three] {
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

// checkCephTell 校验 tell <daemon> <subcmd> [args...]。
// 只放行精确的只读形状；任何未列出的组合一律拒绝。
func (p *Policy) checkCephTell(args []string) error {
	if len(args) < 3 {
		return fmt.Errorf("rejected: ceph tell needs daemon and command")
	}
	sub := args[2]
	rest := len(args) - 3

	// 无参数的只读命令
	switch sub {
	case "dump_historic_ops", "dump_ops_in_flight", "dump_blocked_ops", "status", "version":
		if rest == 0 {
			return nil
		}
		return fmt.Errorf("rejected: ceph tell %q takes no extra arguments", sub)
	case "log":
		// 只允许 log last <N>
		if rest == 2 && args[3] == "last" && isDigits(args[4]) {
			return nil
		}
		return fmt.Errorf("rejected: ceph tell log only supports 'last <N>'")
	case "perf":
		// 只允许 perf dump / perf schema；perf reset 是变更操作
		if rest == 1 && (args[3] == "dump" || args[3] == "schema") {
			return nil
		}
		return fmt.Errorf("rejected: ceph tell perf only supports dump/schema")
	}
	return fmt.Errorf("rejected: ceph tell command %q not in allowlist", sub)
}

// isDigits 判断字符串是否全为数字（用于校验日志行数）。
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (p *Policy) checkKubectl(args []string) error {
	verb := p.kubectlVerb(args)
	switch verb {
	case "get", "describe", "logs":
		return nil
	case "exec":
		return p.checkExecInner(args, "--")
	}
	return fmt.Errorf("rejected: kubectl verb %q not allowed", verb)
}

// containerIDIndex 返回容器 ID 在 args 中的下标（args 为 exec 之后的参数）。
// 需要识别带取值的选项（如 --timeout 10），否则选项的值会被误当成容器 ID。
func containerIDIndex(args []string) int {
	withValue := map[string]bool{
		"--timeout": true, "-t": true,
		"--runtime": true, "-r": true,
		"--runtime-endpoint": true, "-i": true,
		"--image-endpoint": true,
		"--namespace":      true, "-n": true,
		"--user": true, "-u": true,
		"--workdir": true, "-w": true,
		"--env": true, "-e": true,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return i
		}
		if strings.Contains(a, "=") {
			continue
		}
		if withValue[a] && i+1 < len(args) {
			i++
		}
	}
	return -1
}

// checkExecInner 递归校验容器 exec 的内层命令。
//
// `kubectl exec <pod> -- <cmd...>` 与 `crictl/docker exec <id> <cmd...>`
// 的外层动词是只读的，但真正执行的是内层命令。外层通过不代表内层安全，
// 必须把内层当成一条独立命令重新走一遍策略。
func (p *Policy) checkExecInner(args []string, sep string) error {
	idx := -1
	for i, a := range args {
		if a == sep {
			idx = i
			break
		}
	}
	var inner []string
	if idx >= 0 {
		inner = args[idx+1:]
	} else {
		// crictl/docker exec <opts> <container-id> <cmd...>，无分隔符。
		// 先跳过选项找到容器 ID 的下标，其后的所有参数（含内层命令自己的
		// 选项，如 `ceph -s` 的 -s）都原样作为内层命令。
		if i := containerIDIndex(args[1:]); i >= 0 && i+1 < len(args)-1 {
			inner = args[1:][i+1:]
		}
	}
	if len(inner) == 0 {
		return fmt.Errorf("rejected: %s exec requires an inner command", args[0])
	}
	return p.Check(inner)
}

// kubectlVerb 找到真正的动词：跳过以 '-' 开头的选项，
// 以及需要取值的选项（-n <ns>、--namespace <ns> 等）后面的那个值。
func (p *Policy) kubectlVerb(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if (a == "-n" || a == "--namespace") && i+1 < len(args) {
				i++ // 跳过这个选项的取值
			}
			continue
		}
		return a
	}
	return ""
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
		// crictl/docker exec 没有 "--"，内层命令紧跟在容器 ID 之后
		return p.checkExecInner(args, "")
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

// checkLs 允许两种形态，参数都必须在白名单路径内：
//
//	ls -d <path>   判断目录是否存在
//	ls <path>      列出目录内容（用于判断该目录是否真的存放 ceph 日志）
func (p *Policy) checkLs(args []string) error {
	switch {
	case len(args) == 2 && args[0] == "-d":
		if !p.pathAllowed(args[1]) {
			return fmt.Errorf("rejected: path %q outside allowed prefixes", args[1])
		}
		return nil
	case len(args) == 1:
		if !p.pathAllowed(args[0]) {
			return fmt.Errorf("rejected: path %q outside allowed prefixes", args[0])
		}
		return nil
	}
	return fmt.Errorf("rejected: ls must be called as '-d <path>' or '<path>'")
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
