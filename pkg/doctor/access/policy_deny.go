package access

import (
	"fmt"
	"strings"
)

// denyPolicy 是「禁止变更」式只读策略。
//
// 与白名单的取舍：
//   - 白名单（旧）：只允许预先列出的命令。安全，但把诊断能力锁死在
//     「作者想到的场景」里——遇到没预设的故障，模型无手段可查。
//   - 禁止清单（本实现）：拒绝一切会改变集群/系统状态的命令，
//     其余只读命令放行。模型能自己决定查什么，
//     而"不能变更"这条硬约束依然成立。
//
// 判断分三层，任一层命中即拒绝：
//  1. 已知的变更动词（按完整子命令前缀匹配）
//  2. 参数级危险写法（重定向、管道、命令替换等不该出现在 argv 里的东西）
//  3. 未登记的二进制
//
// 注意：这里放行的是「argiv 层面只读」。真正的兜底是 exec 不经过 shell，
// 且 argv 由模型以结构化参数给出，不是拼接的字符串。
type denyPolicy struct {
	// allowedBinaries 是允许执行的程序集合。
	// 仍然需要一份正向清单：未知程序一律不执行，避免误跑 dd/mkfs/rm 之类。
	allowedBinaries map[string]bool
	// mutatingPatterns 是「出现在 argv 里就一定意味着变更」的片段。
	mutatingPatterns []string
}

// DefaultDenyPolicy 返回默认的禁止变更策略。
func DefaultDenyPolicy() *Policy {
	d := &denyPolicy{
		allowedBinaries: map[string]bool{
			// Ceph 及 K8s 侧只读查询
			"ceph": true, "rados": true, "rbd": true, "radosgw-admin": true,
			"kubectl": true, "crictl": true, "docker": true,
			// 主机层观测
			"iostat": true, "mpstat": true, "vmstat": true, "pidstat": true,
			"sar": true, "free": true, "uptime": true, "top": true, "ps": true,
			"df": true, "du": true, "lsblk": true, "blkid": true, "findmnt": true,
			"smartctl": true, "nvme": true, "dmesg": true, "journalctl": true,
			"ss": true, "ip": true, "netstat": true, "ping": true, "ethtool": true,
			// tc 只允许查看队列规则；netem 这类故障注入是网络类问题的直接证据。
			"tc":    true,
			"nproc": true, "lscpu": true, "uname": true, "hostname": true,
			// 文件与文本观测
			"ls": true, "cat": true, "tail": true, "head": true, "grep": true,
			"wc": true, "stat": true, "find": true, "zcat": true, "zgrep": true,
			"sort": true, "uniq": true, "awk": true, "sed": true, "cut": true,
			"tr": true, "column": true, "jq": true, "md5sum": true,
			// 时间与版本
			"date": true, "timedatectl": true, "chronyc": true, "ntpq": true,
			"command": true, "which": true,
			// 仅由 Cluster 在入口节点上使用，参数由程序构造（节点名经白名单校验），
			// 不接受模型直接传入。
			"ssh": true,
		},
		mutatingPatterns: []string{
			// ceph 侧变更
			"osd out", "osd in", "osd rm",
			// crush 只拒绝变更子命令；rule dump / tree / class ls 都是只读，
			// 误拦它们会让模型无法核对 CRUSH 拓扑与失败域。
			"osd crush add", "osd crush remove", "osd crush move",
			"osd crush create-bucket", "osd crush reweight", "osd crush set",
			"osd pool create",
			"osd pool delete", "osd pool set", "osd set", "osd unset",
			"config set", "config rm", "injectargs", "pg repair", "pg scrub",
			"pg deep-scrub", "pool create", "pool delete", "pool set",
			"rbd rm", "rbd create", "rbd resize", "rados rm", "rados put",
			"radosgw-admin user create", "radosgw-admin user rm",
			"mon add", "mon remove", "mgr fail", "osd new", "osd purge",
			// k8s 侧变更
			"kubectl delete", "kubectl apply", "kubectl create", "kubectl edit",
			"kubectl patch", "kubectl scale", "kubectl replace", "kubectl label",
			"kubectl annotate", "kubectl cordon", "kubectl drain", "kubectl rollout",
			"kubectl exec --", // exec 走单独逻辑
			// 容器侧变更
			"crictl rm", "crictl stop", "crictl start", "crictl run",
			"docker rm", "docker stop", "docker start", "docker run", "docker kill",
			// 主机侧变更
			"systemctl start", "systemctl stop", "systemctl restart",
			"systemctl enable", "systemctl disable", "reboot", "shutdown",
			"mount ", "umount ", "mkfs", "dd ", "fdisk", "parted",
		},
	}
	return &Policy{deny: d}
}

// checkDeny 实现禁止清单校验。
func (p *Policy) checkDeny(argv []string) error {
	d := p.deny
	if len(argv) == 0 {
		return fmt.Errorf("rejected: empty argv")
	}

	bin := argv[0]
	if !d.allowedBinaries[bin] {
		return fmt.Errorf("rejected: 程序 %q 不在允许列表内（只读诊断工具可执行）", bin)
	}

	// 参数里不允许出现真正的换行/回车/NUL。argv 不经过 shell，这类字符
	// 只会出现在拼注入的场景里。
	//
	// 但 kubectl 的 jsonpath 表达式需要 \t \n 作为分隔符——它们在这里是
	// 两个字符的反斜杠转义，不是真实控制字符，因此按原始字符判断即可。
	for _, a := range argv {
		if strings.ContainsAny(a, "\n\r\x00") {
			return fmt.Errorf("rejected: 参数含控制字符")
		}
	}

	joined := strings.Join(argv, " ")
	for _, pat := range d.mutatingPatterns {
		needle := strings.TrimSpace(pat)
		if needle == "" {
			continue
		}
		if matchesMutating(joined, needle) {
			return fmt.Errorf("rejected: 命令包含变更操作 %q，本环境只允许只读查询", needle)
		}
	}

	// 路径穿越与敏感文件永远拒绝。
	// 只读不等于可以读密钥：私钥、shadow 之类与诊断无关，且可能被带进模型上下文。
	for _, a := range argv {
		if strings.Contains(a, "..") {
			return fmt.Errorf("rejected: 参数 %q 含路径穿越", a)
		}
		if isSensitivePath(a) {
			return fmt.Errorf("rejected: 参数 %q 指向敏感文件，已拒绝读取", a)
		}
	}

	if err := checkExecInnerDeny(p, argv); err != nil {
		return err
	}

	// tc 只放行查询形态：qdisc/class/filter show。add/del/change/replace 会改配置。
	if bin == "tc" {
		return checkTCReadOnly(argv)
	}

	// ceph tell 的特殊形态：
	//   ceph tell <daemon> <cmd>  —— 无 -f json 之类的后缀
	// 允许常见只读命令，拒绝其它（防止 perf reset / injectargs 之类的漏网）。
	if bin == "ceph" && len(argv) >= 3 && argv[1] == "tell" {
		return checkCephTellDeny(argv)
	}

	return nil
}

// checkTCReadOnly 只允许 tc 的查询子命令。
//
//	tc qdisc show / tc -s qdisc show / tc class show / tc filter show dev X
//
// add、del、change、replace 会修改网络配置，一律拒绝。
func checkTCReadOnly(argv []string) error {
	if len(argv) < 2 {
		return fmt.Errorf("rejected: tc 需要子命令")
	}
	for _, a := range argv[1:] {
		switch a {
		case "add", "del", "delete", "change", "replace", "link":
			return fmt.Errorf("rejected: tc %q 会修改网络配置", a)
		}
	}
	// 必须显式带上查询动词；只有 "tc qdisc" 这种不完整写法一律拒绝。
	for _, a := range argv[1:] {
		switch a {
		case "show", "list", "ls":
			return nil
		}
	}
	return fmt.Errorf("rejected: tc 只允许 show/list 查询，收到 %v", argv[1:])
}

// matchesMutating 判断命令串里是否出现了变更模式。
//
// 按「词边界」匹配，避免 "osd set" 误伤 "osd settings" 这类，
// 也避免 "pg stat" 被 "pg scrub" 之类的模式误判（模式本身是完整子串）。
func matchesMutating(joined, needle string) bool {
	idx := 0
	for {
		i := strings.Index(joined[idx:], needle)
		if i < 0 {
			return false
		}
		pos := idx + i
		end := pos + len(needle)
		// 前面必须是字符串开头或空白
		startOK := pos == 0 || joined[pos-1] == ' ' || joined[pos-1] == '\''
		// 后面必须是结尾、空白，或 needle 本身以空格结尾
		endOK := end == len(joined) || joined[end] == ' ' || joined[end] == '\'' ||
			strings.HasSuffix(needle, " ")
		if startOK && endOK {
			return true
		}
		idx = pos + 1
		if idx >= len(joined) {
			return false
		}
	}
}

// checkCephTellDeny 校验 ceph tell 的只读性。
//
// tell 会直接在 daemon 上执行命令，其中 injectargs / perf reset / config set
// 都具备变更能力，必须按完整子命令精确拒绝。
func checkCephTellDeny(argv []string) error {
	if len(argv) < 3 {
		return fmt.Errorf("rejected: ceph tell 需要 daemon 与命令")
	}
	// 形态：ceph tell <daemon> <cmd> [args...]
	// 因此 argv[0]=ceph, argv[1]=tell, argv[2]=daemon, argv[3]=cmd
	if len(argv) < 4 {
		// 只有 daemon 没有命令，无意义但也无副作用，放行
		return nil
	}
	cmd := argv[3]
	rest := argv[4:]

	// tell 直接作用在 daemon 进程上，这里的命令往往比 CLI 层更危险。
	switch cmd {
	case "injectargs", "config", "bench", "flush_journal",
		"dump_mempools", "get_command_descriptions":
		// config 的只读子命令（get/show）单独放行，其余拒绝
		if cmd == "config" && len(rest) > 0 && (rest[0] == "get" || rest[0] == "show") {
			return nil
		}
		return fmt.Errorf("rejected: ceph tell %q 可能变更状态，已拒绝", cmd)

	case "perf":
		if len(rest) > 0 && (rest[0] == "dump" || rest[0] == "schema") {
			return nil
		}
		return fmt.Errorf("rejected: ceph tell perf 只允许 dump/schema（reset 会清空计数器）")
	}
	return nil
}

// sensitivePathPrefixes 是诊断永远不需要、但泄露代价很高的路径。
var sensitivePathPrefixes = []string{
	"/etc/shadow", "/etc/gshadow", "/etc/sudoers",
	"/root/.ssh", "/home", "/etc/ceph/keyring",
	"/var/lib/ceph/bootstrap",
}

// sensitivePathFragments 命中即拒绝的片段（子串匹配，覆盖相对写法）。
var sensitivePathFragments = []string{
	"id_rsa", "id_ed25519", "id_dsa", ".ssh/", "keyring", "shadow",
}

// isSensitivePath 判断参数是否指向敏感文件。
func isSensitivePath(arg string) bool {
	for _, p := range sensitivePathPrefixes {
		if arg == p || strings.HasPrefix(arg, p+"/") {
			return true
		}
	}
	lower := strings.ToLower(arg)
	for _, frag := range sensitivePathFragments {
		if strings.Contains(lower, frag) {
			return true
		}
	}
	// 允许读取 ceph 配置里的 keyring 路径本身被列出，但不允许读内容：
	// ceph config get 返回的是路径字符串，不含内容，所以上面按文件名拒绝即可。
	return false
}

// checkExecInnerDeny 递归校验容器 exec 的内层命令。
// 外层只读不代表内层安全——kubectl exec pod -- rm -rf 必须被拦住。
func checkExecInnerDeny(p *Policy, argv []string) error {
	if len(argv) < 2 {
		return nil
	}
	bin := argv[0]
	if bin != "kubectl" && bin != "crictl" && bin != "docker" {
		return nil
	}

	var inner []string
	sep := -1
	for i, a := range argv {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep >= 0 {
		inner = argv[sep+1:]
	} else {
		// crictl/docker exec <opts> <id> <cmd...>
		// ceph 相关 exec 由 Cluster.rewrite 生成，这里只处理模型直接给出的形态
		for i, a := range argv {
			if a == "exec" && i+1 < len(argv) {
				pos := containerIDIndex(argv[i+1:])
				if pos >= 0 && i+1+pos+1 < len(argv) {
					inner = argv[i+1+pos+1:]
				}
				break
			}
		}
	}
	if len(inner) == 0 {
		return nil
	}
	// 内层命令同样要过禁止清单；但不能无限递归（内层再 exec 的情况直接拒绝）
	if inner[0] == "kubectl" || inner[0] == "crictl" || inner[0] == "docker" {
		return fmt.Errorf("rejected: 不允许嵌套 exec")
	}
	return p.checkDeny(inner)
}
