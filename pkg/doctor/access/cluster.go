package access

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// executor 是「完整执行」的可替换实现：包含路由改写与真实发送。
// Run 走这条路径，测试注入它即可模拟整个执行过程。
type executor func(ctx context.Context, node string, argv []string) (Output, error)

// sender 是「裸执行」的可替换实现：只把 argv 发到节点，不做路由改写。
// 能力探测与拓扑发现使用它——它们本身就在产生路由所需的 Capabilities，
// 若走 executor 会再次触发 rewrite，形成无限递归。
type sender func(ctx context.Context, node string, argv []string) (Output, error)

// Cluster 是接入层对外的唯一入口。
// 工具层只与此类型交互：策略校验、路由、带超时执行、错误分类、输出截断，
// 以及能力与拓扑的按需缓存都在这里完成。
type Cluster struct {
	conn     *clusterConn
	executor executor
	sender   sender

	// mu 保护下列所有缓存。工具层将来会并发调用 Run，
	// 缓存必须同时支持并发读与写入。
	mu      sync.RWMutex
	nodes   map[string]bool
	caps    map[string]Capabilities
	topo    *Topology
	clients map[string]*ssh.Client

	// toolboxPod / monContainer 缓存容器路由需要定位的目标，避免每次命令都查一遍。
	toolboxPod   string
	monContainer string
}

func newCluster(cc *clusterConn) *Cluster {
	c := &Cluster{
		conn:    cc,
		nodes:   map[string]bool{cc.cfg.Host: true},
		caps:    map[string]Capabilities{},
		clients: map[string]*ssh.Client{},
	}
	c.executor = c.realExecute
	c.sender = c.sendViaSSH
	return c
}

// setExecutor 替换「完整执行」层，仅用于测试注入。
func (c *Cluster) setExecutor(e executor) { c.executor = e }

// setSender 替换「裸执行」层，仅用于测试注入。
func (c *Cluster) setSender(s sender) { c.sender = s }

// Run 在指定节点上执行一条只读命令。
// 顺序：原始 argv 过策略 → 校验节点 → 路由（必要时改写为容器 exec）
// → 执行 → 截断输出 → 回填 Node/Command。
func (c *Cluster) Run(ctx context.Context, node string, argv []string) (Output, error) {
	// 策略只校验工具「想做什么」，路由改写后的 argv 在 realExecute 中会再校验一次。
	if err := c.conn.cfg.Policy.Check(argv); err != nil {
		return Output{}, &ToolError{Kind: KindFailed, Message: err.Error()}
	}
	c.mu.RLock()
	known := c.nodes[node]
	c.mu.RUnlock()
	if !known {
		return Output{}, unavailable("未知节点 %q", node)
	}
	out, err := c.executor(ctx, node, argv)
	if err != nil {
		// 执行层可能只返回原始错误（例如注入的假实现），这里统一补一次语义分类。
		// 已是 *ToolError 的错误（超时/策略等）保持原样。
		var te *ToolError
		if !errors.As(err, &te) {
			err = classifyError(err, out.Stderr, out.ExitCode)
		}
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

// realExecute 先做路由改写，再通过链式 SSH 执行。
// 超时覆盖「定位容器/pod + 命令本身」的整个过程。
func (c *Cluster) realExecute(ctx context.Context, node string, argv []string) (Output, error) {
	timeout := c.conn.cfg.CommandTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	// 整个改写 + 执行共享同一个超时预算
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	finalArgv, err := c.rewrite(ctx, node, argv)
	if err != nil {
		return Output{}, err
	}
	// 改写后的 argv 同样必须通过策略（kubectl exec / crictl exec 均在白名单内）
	if err := c.conn.cfg.Policy.Check(finalArgv); err != nil {
		return Output{}, &ToolError{Kind: KindFailed, Message: err.Error()}
	}
	return c.sender(ctx, node, finalArgv)
}

// execSSH 在已解析的节点上跑一条命令串。
func (c *Cluster) execSSH(ctx context.Context, node, cmd string) (Output, error) {
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

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
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
			if errors.As(err, &exitErr) {
				out.ExitCode = exitErr.ExitStatus()
				return out, classifyError(nil, out.Stderr, out.ExitCode)
			}
			return out, classifyError(err, out.Stderr, 0)
		}
		return out, nil
	}
}

// rewrite 根据节点的能力决定命令到底在哪里执行。
//
//	host-ceph     → 原样在宿主机执行
//	log-only      → 返回 unavailable，让模型改走日志工具
//	toolbox       → kubectl -n rook-ceph exec <pod> -- <argv...>
//	container-exec→ crictl|docker exec <容器ID> <argv...>
//
// 只有 ceph CLI 需要被改写；iostat、tail、ls 等宿主机工具始终在宿主机执行。
func (c *Cluster) rewrite(ctx context.Context, node string, argv []string) ([]string, error) {
	if len(argv) == 0 || argv[0] != "ceph" {
		return argv, nil
	}

	caps := c.Capabilities(ctx, node)
	switch caps.Backend {
	case "host-ceph":
		return argv, nil
	case "toolbox":
		pod, err := c.toolboxPodName(ctx, node)
		if err != nil {
			return nil, err
		}
		rewritten := append([]string{"kubectl", "-n", caps.Namespace(), "exec", pod, "--"}, argv...)
		return rewritten, nil
	case "container-exec":
		id, err := c.monContainerID(ctx, node, caps.ContainerRT)
		if err != nil {
			return nil, err
		}
		rewritten := append([]string{caps.ContainerRT, "exec", id}, argv...)
		return rewritten, nil
	default:
		return nil, unavailable("节点 %q 无法执行 ceph 命令（后端 %q），请改用日志类工具", node, caps.Backend)
	}
}

// toolboxPodName 查找并缓存 rook toolbox pod 名。
func (c *Cluster) toolboxPodName(ctx context.Context, node string) (string, error) {
	c.mu.RLock()
	pod := c.toolboxPod
	c.mu.RUnlock()
	if pod != "" {
		return pod, nil
	}
	ns := c.Capabilities(ctx, node).Namespace()
	argv := []string{"kubectl", "-n", ns, "get", "pod",
		"-l", "app=rook-ceph-tools", "-o", "jsonpath={.items[0].metadata.name}"}
	out, err := c.Run(ctx, node, argv)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(out.Stdout)
	if name == "" {
		return "", unavailable("节点 %q 上未找到 rook-ceph-tools pod", node)
	}
	c.mu.Lock()
	c.toolboxPod = name
	c.mu.Unlock()
	return name, nil
}

// monContainerID 查找并缓存 mon 容器 ID，供 crictl/docker exec 使用。
func (c *Cluster) monContainerID(ctx context.Context, node, rt string) (string, error) {
	c.mu.RLock()
	cid := c.monContainer
	c.mu.RUnlock()
	if cid != "" {
		return cid, nil
	}
	var argv []string
	switch rt {
	case "crictl":
		argv = []string{"crictl", "ps", "--name", "rook-ceph-mon", "-q"}
	case "docker":
		argv = []string{"docker", "ps", "--filter", "name=rook-ceph-mon", "-q"}
	default:
		return "", unavailable("节点 %q 的容器运行时 %q 不受支持", node, rt)
	}
	out, err := c.Run(ctx, node, argv)
	if err != nil {
		return "", err
	}
	// 可能有多个 mon 容器，取第一行即可（任一 mon 都能回答集群级查询）
	id := parseFirstLine(out.Stdout)
	if id == "" {
		return "", unavailable("节点 %q 上未找到 rook-ceph-mon 容器", node)
	}
	c.mu.Lock()
	c.monContainer = id
	c.mu.Unlock()
	return id, nil
}

// parseFirstLine 取输出中第一个非空行。
func parseFirstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			return l
		}
	}
	return ""
}

func (c *Cluster) clientFor(ctx context.Context, node string) (*ssh.Client, error) {
	c.mu.RLock()
	cl, ok := c.clients[node]
	c.mu.RUnlock()
	if ok {
		return cl, nil
	}
	cl, err := c.conn.dialNode(ctx, node)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// 双检：并发建连时保留先注册的那条，关闭多余的
	if existing, exists := c.clients[node]; exists && existing != c.conn.entry {
		if cl != c.conn.entry {
			cl.Close()
		}
		return existing, nil
	}
	c.clients[node] = cl
	return cl, nil
}

// Capabilities 返回节点的能力，结果按节点缓存。
func (c *Cluster) Capabilities(ctx context.Context, node string) Capabilities {
	c.mu.RLock()
	caps, ok := c.caps[node]
	c.mu.RUnlock()
	if ok {
		return caps
	}
	tr := &clusterTransport{cluster: c, node: node}
	caps = probeCapabilitiesWithProgress(ctx, tr, c.conn.cfg.OnProgress)
	c.mu.Lock()
	c.caps[node] = caps
	c.mu.Unlock()
	return caps
}

// Topology 返回集群拓扑，首次调用时发现并缓存。
// 以入口节点为数据源，发现到的节点全部登记，之后其余节点即可直接 Run。
func (c *Cluster) Topology(ctx context.Context) (*Topology, error) {
	c.mu.RLock()
	cached := c.topo
	c.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	entry := c.conn.cfg.Host

	// 先取 fsid，失败则不阻塞拓扑（置空即可）
	var fsid string
	if out, err := c.Run(ctx, entry, []string{"ceph", "fsid"}); err == nil {
		fsid = strings.TrimSpace(out.Stdout)
	}

	out, err := c.Run(ctx, entry, []string{"ceph", "osd", "tree", "-f", "json"})
	if err != nil {
		return nil, err
	}
	nodes, err := parseOSDTree(out.Stdout)
	if err != nil {
		return nil, err
	}

	tr := &clusterTransport{cluster: c, node: entry}
	if p := c.conn.cfg.OnProgress; p != nil {
		p(fmt.Sprintf("发现 %d 个 OSD，逐个查询所在节点与磁盘", len(nodes)))
	}
	topo := buildTopology(ctx, tr, fsid, nodes)

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range topo.Nodes {
		c.nodes[n.Name] = true
	}
	c.topo = &topo
	return c.topo, nil
}

// SecurityNotes 返回本次连接在安全上的放宽项（例如未校验主机键）。
// 这些提示会写进报告，让读者知道结论是在什么信任前提下得出的。
func (c *Cluster) SecurityNotes() []string {
	if c.conn.cfg.hostKeyNote == "" {
		return nil
	}
	return []string{c.conn.cfg.hostKeyNote}
}

// Nodes 返回已知节点列表（入口节点在前），顺序稳定便于展示。
func (c *Cluster) Nodes() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.nodes))
	if c.conn.cfg.Host != "" && c.nodes[c.conn.cfg.Host] {
		out = append(out, c.conn.cfg.Host)
	}
	for n := range c.nodes {
		if n != c.conn.cfg.Host {
			out = append(out, n)
		}
	}
	return out
}

// Close 关闭所有派生连接以及入口连接。
func (c *Cluster) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cl := range c.clients {
		if cl != c.conn.entry {
			cl.Close()
		}
	}
	return c.conn.Close()
}

// clusterTransport 把 Cluster 适配成探测/拓扑用的 transport。
// 探测命令的失败在探测层按退出码处理，因此这里把错误统一折算为非零退出码。
type clusterTransport struct {
	cluster *Cluster
	node    string
}

// exec 执行一条探测命令。
//
// 关键约束：这里必须绕过路由（rewrite）。rewrite 需要先知道 Capabilities
// 才能决定命令在哪执行，而探测本身就是产生 Capabilities 的过程——
// 一旦探测走 rewrite，就会形成
// Capabilities → probe → exec → rewrite → Capabilities 的无限递归。
//
// 因此探测命令一律按原样在该节点上执行：它们都是「宿主机上的只读检查」
// （ceph --version / kubectl get / crictl ps / ls / command -v），
// 语义上本来就属于宿主机层，不需要、也不能被改写到容器里。
// 策略校验仍然执行，未登记的 argv 会被拒绝。
func (t *clusterTransport) exec(ctx context.Context, argv []string) (string, string, int) {
	if err := t.cluster.conn.cfg.Policy.Check(argv); err != nil {
		return "", err.Error(), 1
	}
	out, err := t.cluster.sender(ctx, t.node, argv)
	if err != nil {
		return out.Stdout, out.Stderr, nonZero(out.ExitCode)
	}
	return out.Stdout, out.Stderr, out.ExitCode
}

// sendViaSSH 是默认的 sender：加超时后经 SSH 发送。
// sender 只服务于探测与拓扑这类快速查询，因此用较短的 ProbeTimeout；
// 慢的集群查询走 Run，由 CommandTimeout 控制。
func (c *Cluster) sendViaSSH(ctx context.Context, node string, argv []string) (Output, error) {
	timeout := c.conn.cfg.ProbeTimeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.execSSH(ctx, node, BuildCommand(argv))
}

// nonZero 保证出错的命令返回非零退出码，供探测层判断「不可用」。
func nonZero(code int) int {
	if code == 0 {
		return 1
	}
	return code
}
