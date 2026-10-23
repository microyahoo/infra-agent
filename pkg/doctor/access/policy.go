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

func (p *Policy) checkKubectl(args []string) error {
	verb := p.kubectlVerb(args)
	switch verb {
	case "get", "describe", "logs", "exec":
		return nil
	}
	return fmt.Errorf("rejected: kubectl verb %q not allowed", verb)
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
