# Ceph 集群自动诊断 Agent — 设计文档

- 日期：2026-10-23
- 状态：待实施
- 目标仓库：`github.com/microyahoo/infra-agent`

## 1. 背景与目标

当前仓库的 `analyze` 命令是 **Kubernetes 视角**的检查：通过 kubeconfig 连 K8s API，分析 Pod / Node / PVC / Rook 容器状态。`pkg/ceph` 下有版本感知诊断的雏形（版本探测、日志特征反查源码、slow ops / perf counters 采集），但 `Diagnoser` 是硬编码的六步流程，不是自主调查。

要解决的真实问题是：运维手上有 **40 套 Rook Ceph 存储集群**，出现问题时需要人工 SSH 进集群、翻日志、看状态、关联到具体磁盘，最后自己判断根因。这个过程慢、依赖个人经验、结论不可追溯。

**目标**：给一条命令和首节点地址，agent 自己进集群、自己决定查什么、给出**每条结论都挂着真实命令输出**的诊断报告。

**对齐的参考形态**：群里的 `DrTraining 智能助手` —— 对话式、自行查证、会给结论、证据不足时会说证据不足、**只查询和诊断，不做变更**。

## 2. 范围

### 2.1 第一版包含

- CLI 形态：`infra-agent doctor --host <首节点IP>`
- 包名：`pkg/doctor`，命令名 `doctor`
- 报告语言：中文，命令与输出保持原文
- SSH 链式接入，首节点为跳板，其余节点免密可达
- 四类问题的诊断：性能（slow ops / 延迟）、容量（nearfull / 池满）、可用性（OSD/PG 异常）、日常体检
- LLM 工具调用循环，模型自主决定调查路径
- 本地源码目录反查（`--ceph-src`）
- 结构化报告：text / brief / json 三种渲染
- 全程只读

### 2.2 第一版不包含

- Prometheus / Grafana 指标接入
- 飞书 / Slack 机器人
- 多集群并发调度与横向对比（仅预留接口）
- 自动修复（硬性排除）
- Web UI
- K8s 资源全貌分析（仍归 `analyze` 命令）

## 3. 总体架构

### 3.1 一次诊断的动线

```
接入 → 能力探测 → 拓扑发现
  ↓
确定性快照（ceph -s / df / osd tree / health）
  ↓
调查循环（LLM 决定下一步，工具执行，证据回灌）
  ↓
结构化结论（强制 schema）→ 证据回填 → 渲染报告
```

「确定性快照」是在循环前固定采集一轮基础数据，压缩成集群摘要写入 system prompt。作用是让模型一上来就掌握基本事实，不必浪费前几轮工具调用去发现「有几个 OSD、健康状态如何、容量多少」。它不是第二套诊断逻辑，不产出结论。

### 3.2 包结构

```
pkg/doctor/
  access/    SSH 连接池 · 首节点跳板 · 拓扑发现 · 能力探测 · 只读沙箱
  tools/     工具注册表 + 领域工具（ceph / log / host / container / source）
  llm/       OpenAI 兼容的工具调用客户端
  loop/      调查循环 · 预算控制 · 证据链
  report/    结构化结论 + 三种渲染
  doctor.go  编排入口
cmd/doctor.go
```

### 3.3 分层边界

| 层 | 职责 | 不知道什么 |
|---|---|---|
| `access` | 在某节点上安全执行一条 argv | Ceph 语义 |
| `tools` | 把 argv 结果包装成模型可用的摘要 | 循环、模型 |
| `llm` | 与模型通信，处理 tool_use 协议 | 集群 |
| `loop` | 驱动模型 ↔ 工具往返，控制预算 | 具体工具实现 |
| `report` | 把证据链 + 模型结论渲染成报告 | 不执行任何命令 |

### 3.4 关键抽象

```go
// access 层唯一出口
type Runner interface {
    Run(ctx context.Context, node string, argv []string) (Output, error)
}

// llm 层，可替换（OpenAI 官方 / 中转 / 未来其他）
type ChatModel interface {
    Chat(ctx context.Context, msgs []Message, tools []ToolSpec) (Reply, error)
}

// tools 层
type Tool interface {
    Name()        string
    Description() string
    Schema()      json.RawMessage
    Execute(ctx context.Context, args json.RawMessage) (Result, error)
}
```

`Runner` 与 `ChatModel` 均为接口，使整条链路可在**无集群、无 API key** 的条件下用回放器完整运行。

### 3.5 与既有代码的关系

旧代码保留不动，作为参考实现，不再是主路径：

- `pkg/ceph/perf.go` 的 slow ops / perf counters 解析逻辑移植到 `pkg/doctor/tools`
- `pkg/ceph/source.go` 的源码反查思路移植到 `pkg/doctor/tools`（改为本地目录优先）
- `pkg/ceph/version.go` 的版本探测逻辑移植
- `pkg/ceph/toolbox.go` 的 K8s exec 路径保留，作为 `access` 的 `toolbox` 后端参考
- `cmd/analyze.go`、`pkg/analyzer/*` 保持原样，两条命令互不干扰

## 4. 接入层（`pkg/doctor/access`）

### 4.1 连接模型：链式 SSH

不使用「在首节点上执行 `ssh node2 "..."`」的方式。原因是远程 shell 会引入引号、变量展开、管道等转义问题，且依赖首节点安装 ssh 客户端。

采用链式 SSH：本地建立到首节点的 `ssh.Client`，在其上通过 `Dial("tcp", "<node2>:22")` 建立到其余节点的连接，得到独立的 `*ssh.Client`。命令行参数以 argv 形式传递，不经过任何 shell。

首节点到其余节点免密（已确认现网如此）。节点间网络不通视为故障证据，记录入证据链，而非接入失败。

### 4.2 能力探测

每节点按固定顺序探测，记录成功项：

| 顺序 | 后端标识 | 探测方式 | 能力 |
|---|---|---|---|
| 1 | `host-ceph` | `ceph --version` | 全部 ceph CLI |
| 2 | `toolbox` | `kubectl -n rook-ceph get pod -l app=rook-ceph-tools` 返回 Running 的 pod | 全部 ceph CLI |
| 3 | `container-exec` | `crictl ps` 或 `docker ps` | 全部 ceph CLI |
| 4 | `log-only` | 日志目录存在 | 仅日志读取 |

输出 `Capabilities`：

```go
type Capabilities struct {
    CephCLI     bool
    CephVersion string
    Backend     string            // host-ceph / toolbox / container-exec / log-only
    Kubectl     bool
    ContainerRT string            // crictl / docker / none
    LogDirs     []string
    HostTools   map[string]bool   // iostat / smartctl / dmesg / lsblk ...
}
```

`Capabilities` 用于三处：注册哪些工具、写入 system prompt（告知模型能力边界）、写入报告（声明结论基于什么条件得出）。

`log-only` 是合法降级态：`ceph -s` 不可用，但 daemon 日志仍可读，仍能产出部分结论。循环必须知道某工具不可用，否则会反复重试。

### 4.3 拓扑发现

从 slow op / OSD 编号映射到宿主机物理盘是性能诊断的必要链路：

```
osd.5 → host (node-7 / 10.20.30.7) → device (sdc) → iostat / smartctl / dmesg
```

通过 `ceph osd tree -f json` 与 `ceph osd metadata <id> -f json` 一次性拉取全量，建立映射：

```go
type Topology struct {
    FSID  string
    Nodes []Node
    OSDs  []OSDPlacement
}

type OSDPlacement struct {
    ID          int
    Host        string
    Device      string   // 解析后的物理盘，如 sdc
    RawDevice   string   // 原始值，可能是 /dev/mapper/ceph--xxxx
    DBDevice    string
    ContainerID string
}
```

`ceph-volume lvm` 场景下 `bluestore_bdev_devices` 返回的是 `/dev/mapper/ceph--<osd-id>-lv-...` 形式的 LVM 路径，需通过 `lsblk -no PKNAME` 反查物理盘。映射失败时保留 `RawDevice`，工具在无法执行 `smartctl` 时明确返回 `unavailable` 而非静默失败。

### 4.4 只读沙箱

所有命令以 argv 形式执行，**全链路不使用 shell**。远程 grep 一律禁止，日志过滤在 Go 侧完成，避免故障集群上额外的 IO 压力。

允许清单按程序名 + 子命令登记，未登记组合直接拒绝：

```
ceph     : -s, status, health, df, osd (tree/df/metadata/ok-to-stop/ls), pg (stat/dump_stuck/query), mon, mgr, tell, log, versions, daemon, config get
crictl   : ps, inspect, logs, exec
docker   : ps, inspect, logs, exec
kubectl  : get, describe, logs, exec
host     : iostat, smartctl (-a/-H/-i), dmesg (-T), df, free, uptime, lsblk, ip
```

变更类动作（`osd out/in/rm/crush/pool create/set`、`kubectl delete/apply` 等）在 `access` 层拒绝，工具层无法调用到写操作。这层拦截不依赖提示词约束。

### 4.5 有界执行

| 约束 | 默认值 | 说明 |
|---|---|---|
| 单命令超时 | 30s | 故障集群 `ceph -s` 可能长时间无响应 |
| 单命令输出上限 | 256 KB | 超出保留头尾，标记 `Truncated` |
| 节点并发 | 8 | |
| 连接复用 | 每节点 1 个 `*ssh.Client` | |

超时具有诊断含义：`ceph -s` 超时指示 mon 响应异常，须作为 `timeout` 类型返回，与 `failed`、`empty` 明确区分。

### 4.6 凭据

- 私钥路径 / ssh-agent / 密码来自配置或环境变量，不入日志、不入报告
- 首节点 host key：支持 `--known-hosts`；未提供时 TOFU 并将指纹写入报告警示
- 链式节点 host key：优先匹配首节点 `~/.ssh/known_hosts`，否则本次运行内 TOFU 并记录指纹

### 4.7 对外接口

```go
func Dial(ctx context.Context, cfg Config) (*Cluster, error)
func (c *Cluster) Run(ctx context.Context, node string, argv []string) (Output, error)
func (c *Cluster) Capabilities(node string) Capabilities
func (c *Cluster) Topology() Topology
```

`tools` 层仅依赖以上接口，不感知连接方式与路径差异。

## 5. 工具层（`pkg/doctor/tools`）

### 5.1 工具清单（12 个）

**集群全景**

| 工具 | 底层 | 返回重点 |
|---|---|---|
| `ceph_status` | `ceph -s -f json` + `ceph health detail` | health 码与明细、mon quorum、OSD up/in、PG 状态分布、容量、slow op 计数 |
| `osd_tree` | `ceph osd tree -f json` + `ceph osd df -f json` | OSD→host→device 映射、各 OSD 容量/使用率/权重 |
| `pool_info` | `ceph df detail -f json` + `ceph osd pool autoscale-status -f json` | 池容量、PG 数、PG 数与目标值的偏差 |

**可用性 / PG**

| 工具 | 底层 |
|---|---|
| `pg_detail` | `ceph pg stat` + `ceph pg dump_stuck` + 按需 `ceph pg <pgid> query` |
| `osd_detail` | `ceph osd metadata <id>` + `ceph tell osd.<id> status` |
| `daemon_log` | 按 5.4 的降级顺序读取指定 daemon 日志 |

**性能**

| 工具 | 底层 |
|---|---|
| `slow_ops` | `ceph tell osd.<id> dump_historic_ops` / `dump_ops_in_flight`，解析 op 阶段时间线 |
| `perf_counters` | `ceph daemon osd.<id> perf dump` 或 `ceph tell` 等价路径 |
| `host_io` | `iostat -x` / `smartctl -a` / `dmesg -T`，须指定 node + device |

**日志 / 宿主机**

| 工具 | 说明 |
|---|---|
| `read_log` | node + path + 三选一（tail lines / grep pattern / since）。路径限定白名单前缀 |
| `host_check` | 内存、负载、文件系统使用率、内核版本、系统日志错误，一次采集 |

**源码反查**

| 工具 | 说明 |
|---|---|
| `ceph_source_lookup` | 日志片段 → 当前版本源码位置 + 上下文片段。见 6.5 |

### 5.2 返回结构

```go
type Result struct {
    Summary    string   // 一句话结论，给模型
    Data       any      // 结构化细节，按需读取
    EvidenceID string   // 指向证据链原始输出
    Truncated  bool
    Hints      []string // 下一步可执行的具体查询
}
```

`Hints` 承载经验：工具在发现异常时直接给出下一跳建议（含节点名与参数），避免把诊断经验堆在提示词中。例如 `osd_detail` 发现心跳抖动时返回：

```
osd.7 心跳抖动，位于 node-3 / sdd
→ host_io(node=node-3, device=sdd)
→ read_log(node=node-3, path=/var/log/ceph/<fsid>/ceph-osd.7.log, lines=500)
```

### 5.3 错误分类

| 类型 | 模型可见措辞 | 示例 |
|---|---|---|
| `timeout` | 命令 30s 未返回，通常意味着对应组件无响应 | `ceph -s` → mon 卡住 |
| `unreachable` | 节点不可达 | 网络问题，本身即诊断结论 |
| `unavailable` | 该集群无 kubectl，无法执行容器内命令 | 能力缺失，模型应改换路径 |
| `failed` | 原始 stderr | 命令自身报错 |

禁止将超时渲染为 `context deadline exceeded` 之类的底层错误描述。

### 5.4 日志定位降级顺序

```
1. /var/log/ceph/<fsid>/ceph-osd.<id>.log
2. /var/lib/rook/rook-ceph/logs/
3. kubectl -n rook-ceph logs <pod> -c <container>
4. crictl logs <container-id>
```

依次尝试，首次成功即停止，并在 `Summary` 中注明实际使用的路径。

### 5.5 明确不做

- 不接 Prometheus / Grafana
- 不做 K8s 资源全貌（Pod / PVC）分析
- 不做跨集群对比
- 不包含任何变更类工具

## 6. 调查循环（`pkg/doctor/loop`）

### 6.1 循环形态

system prompt 包含角色约束、集群摘要、能力清单、编号规则。每一轮为一次模型调用，模型可返回一个或多个 `tool_use`；工具结果以摘要 + Hints 形式回灌。

### 6.2 预算

| 参数 | 默认 | 说明 |
|---|---|---|
| 最大轮数 | 20 | 一轮 = 一次模型调用 |
| 最大工具调用数 | 60 | |
| 单工具调用上限 | 5 | 防止对同一工具反复调用 |
| 无进展判定 | 连续 2 轮无新 Evidence | 触发收尾 |
| 上下文上限 | 模型窗口 80% | 超出触发压缩 |

「无进展」定义为连续两轮未产生新的 `EvidenceID`，或连续两轮调用参数完全相同。

### 6.3 终止条件

1. **正常完成**：模型不再返回 `tool_use`，直接给出结构化结论
2. **预算耗尽**：强制收尾，要求模型输出「可确定结论 + 已排除假设 + 缺失证据及获取方式」
3. **用户中断**：已收集证据仍渲染为报告

预算耗尽不等同于诊断失败。强制收尾的指令明确禁止继续调用工具，并要求标注不确定性。

### 6.4 上下文压缩

1. 工具结果始终以 `Summary` 优先，`Data` 按需
2. 超过 8 轮的旧工具结果，替换为「已查询 X，关键发现 Y」（复用模型自身已产出的文本，不额外调用）
3. 原始输出永不进入上下文，仅存于证据链

### 6.5 源码反查

- 源码位置来自 `--ceph-src`（内网目录，按版本分目录）或配置项 `ceph.source_dir`
- 目录优先，本地 `git grep` 或 `grep -r` 直接检索，无网络依赖
- 版本取自 `ceph versions`，选择匹配目录
- 外网 clone 作为兜底，默认关闭
- 反查失败不阻塞主流程，作为 `unavailable` 返回

### 6.6 证据链

```go
type Evidence struct {
    ID        string            // ev-001
    Round     int
    Tool      string
    Args      map[string]any
    Command   string            // 实际 argv（脱敏后）
    Node      string
    StartedAt time.Time
    Duration  time.Duration
    ExitCode  int
    RawOutput string
    Summary   string
    Error     *ToolError
}
```

每条证据独立存储，报告通过 ID 引用。

## 7. 报告（`pkg/doctor/report`）

### 7.1 三种渲染

```bash
infra-agent doctor --host 10.20.30.1                  # 完整人读报告
infra-agent doctor --host 10.20.30.1 --format brief   # 值班视角
infra-agent doctor --host 10.20.30.1 --format json    # 机器消费
```

JSON 为主格式，text 与 brief 由其渲染。多集群报告、飞书通知、后续 Web UI 复用同一结构。

### 7.2 报告结构

```
标题     集群 FSID · 健康状态 · 时间
结论     一句话 headline
严重程度 high / critical / ...
影响     影响范围与数据安全评估
判断依据 证据表格（证据 ID · 执行的命令 · 结果摘要）
已排除   假设 + 对应证据
建议操作 风险分级 + 命令文本（不执行）
未能确定 缺失的证据 + 获取方式
```

### 7.3 模型输出为结构化结论，由代码渲染

模型通过强制 schema 的工具返回结论对象：

```json
{
  "severity": "high",
  "headline": "...",
  "impact": "...",
  "evidence_refs": ["ev-003", "ev-005"],
  "ruled_out": [{"hypothesis": "...", "ref": "ev-001"}],
  "recommendations": [{"risk": "low", "action": "...", "command": "..."}],
  "unknowns": [{"what": "...", "why": "...", "how": "..."}]
}
```

渲染时按 `evidence_refs` 从证据链回填真实命令与原始输出。引用的 ID 不存在时判定为失败并重试。模型不得自行抄写证据文本。

### 7.4 `unknowns` 区块

报告必须包含「未能确定」区块，不允许省略。若模型认为无缺失项，须显式填写「无」。prompt 中明确提示：声称完全确定通常是过度自信。

### 7.5 多集群（预留）

`--clusters clusters.yaml` 输出总览表 + 每集群详情。第一版只留接口与总览表渲染，不实现并发调度。

## 8. 配置与安全

### 8.1 配置示例

```yaml
ssh:
  user: root
  private_key: ~/.ssh/id_rsa
  known_hosts: ~/.ssh/known_hosts
  command_timeout: 30s
  max_output_bytes: 262144

llm:
  base_url: https://api.openai.com/v1
  model: gpt-4o
  api_key_env: OPENAI_API_KEY
  max_rounds: 20
  max_tool_calls: 60

ceph:
  source_dir: /data/ceph-src
  source_repo: ""
  log_paths:
    - /var/log/ceph
    - /var/lib/rook/rook-ceph/logs

readonly:
  allow_host_tools: [iostat, smartctl, dmesg, df, free, uptime, lsblk]
```

密钥仅从环境变量读取，配置文件中只出现变量名。

### 8.2 安全边界

- 全链路 argv，无 shell
- 命令白名单，未登记即拒绝
- 只读策略在 `access.Run()` 拦截，工具层无法触达写操作
- 报告中的建议命令为纯文本，无执行入口
- 审计：每次运行的完整 argv 序列落盘 `~/.infra-agent/runs/<timestamp>.json`
- 输出与报告中匹配密码/密钥模式的字符串脱敏

## 9. 测试策略

| 层 | 方法 | 外部依赖 |
|---|---|---|
| `access` 沙箱 | 表驱动，断言 allow / deny | 无 |
| `access` 拓扑解析 | 真实 `ceph osd metadata` JSON fixture | 无 |
| `tools` | 假 `Runner` 返回录制输出，断言 Summary / Hints / 错误分类 | 无 |
| `loop` | 假 `ChatModel` 按脚本回放，断言预算、终止、证据链 | 无 |
| `report` | 给定证据链与结论对象，断言三种渲染 | 无 |
| 端到端 | 回放器整体回放一次真实会话 | 无 |
| 真集群 | `make smoke CLUSTER=<ip>` | 需要集群 |

核心方法：对一套真实集群录制完整会话（argv 序列 + 输出），后续开发与回归全部基于回放。录制样本同时作为 40 套集群的标准基线，改动 agent 后先跑全量回放。

回放数据存放于 `testdata/replay/<cluster-id>/`，格式为 JSON 行，每行一条 `{node, argv, stdout, stderr, exit, duration}`。

## 10. 里程碑

| 阶段 | 内容 | 验收 |
|---|---|---|
| M1 | `access` 层：链式 SSH、能力探测、拓扑发现、只读沙箱 | 对真集群能列出节点与 OSD→盘映射，写操作被拒绝 |
| M2 | `tools` 层 12 个工具 + 返回结构与错误分类 | 用回放数据，每个工具产出正确的 Summary 与 Hints |
| M3 | `llm` + `loop`：工具调用循环、预算、证据链 | 假模型跑通三类终止条件 |
| M4 | `report`：三种渲染 + 证据回填 | 给定证据链渲染出完整报告 |
| M5 | 端到端：`infra-agent doctor` 接入首节点跑通四类场景 | 真集群上产出带证据的报告 |
| M6 | 录制 40 套集群基线样本，接入回归 | 全量回放通过 |

## 11. 风险与对策

| 风险 | 对策 |
|---|---|
| 模型在故障集群上反复调用无效工具 | 单工具上限 5 次；无进展判定；工具 `unavailable` 明确告知能力缺失 |
| 大集群上下文爆炸 | Summary 优先；旧结果降级；原始输出不入上下文 |
| LVM 设备映射失败导致盘级诊断失效 | 保留 `RawDevice`；`lsblk` 反查；失败时报 `unavailable` 而非静默 |
| 故障集群上命令超时拖垮调查 | 30s 超时；超时作为诊断证据而非错误 |
| 模型伪造证据 | 证据由代码从证据链回填；引用不存在的 ID 判定失败 |
| 首节点 host key 未校验 | 支持 `known_hosts`；TOFU 时在报告顶部警示并记录指纹 |
| 源码版本与集群不一致 | 版本取自 `ceph versions`；目录匹配失败时报 `unavailable` |

## 12. 环境假设与核对项

以下三项依赖现网实际情况，实施时按「默认决策 + 探测降级」处理，不阻塞开发：

| 项 | 默认决策 | 探测失败时的行为 |
|---|---|---|
| 内网源码目录组织形式 | 按版本 checkout 的源码树，用 `grep -r` 检索；检测到 `.git` 且为 bare repo 时改用 `git grep` | `ceph_source_lookup` 返回 `unavailable`，主流程继续 |
| 节点是否均安装 ceph 客户端 | 按 4.2 顺序探测，不假设 | 落到 `log-only`，`ceph_status` 等工具返回 `unavailable`，模型转用日志证据 |
| `smartctl` 是否可用 | 在 `host_check` 中探测，记录进 `Capabilities.HostTools` | `host_io` 的 SMART 部分返回 `unavailable`，仍返回 iostat / dmesg 结果 |

M1 阶段对首套集群执行一次探测，把三项的实际结果补写进本文档。