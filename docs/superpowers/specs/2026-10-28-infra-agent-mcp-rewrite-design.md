# infra-agent 重做 — MCP server + Skill 驱动 — 设计文档

- 日期：2026-10-28
- 状态：待实施
- 目标仓库：`github.com/microyahoo/infra-agent`
- 参考形态：`/root/go/src/deeproute/aice-workspace`（Python + FastMCP + SKILL.md）

## 1. 背景与目标

`pkg/doctor` 这条 Ceph 诊断线已经能工作：SSH 链式接入、16 个只读工具、对话式调查循环、飞书机器人。但它长在一个更大的、已经废弃的产品线旁边，并且积累了一批结构问题：

- **配置层有三套**：`cmd/doctor.go` 的 `doctorOptions`（只装 flag）、`cmd/doctor_config.go` 的 `resolvedDoctorOptions`（合并结果）、`cmd/serve.go` 的 `loadBotConfig`（自己又解析一遍 viper）
- **诊断知识散落**：一部分在 `agent_prompt.go` 的字符串里，一部分在 `correlate.go` 的正则里，一部分在工具的 `Description()` 里
- **能力只能内置消费**：想给 Cursor / Dify 用，没有任何对外接口
- **死代码与失效参数**：`Profile`、`MaxPerTool` 解析了从没生效；`snapshotRunner` 零引用；`CollectSnapshot` 丢弃参数；`turnStart` 算完即弃
- **旧产品线占一半仓库**：`pkg/analyzer`、`pkg/ai`、`pkg/diagnosis`、`pkg/executor`、`pkg/kubernetes`、`pkg/notifier`、`pkg/patrol` 与小半套 `cmd/`，约 3000 行、零测试、与 doctor 完全无关

**目标**：按 aice-workspace 的组织原则重做整个仓库。

1. 入口薄，能力分层，依赖单向
2. 诊断知识只有一份事实源（SKILL.md），两个消费者
3. 同一批工具同时服务内置 agent 与外部 agent（MCP）
4. 配置只有一套结构、一个加载器
5. 仓库里只保留真实在用的东西

**明确不做**：保留 `analyze` / `patrol` 等旧能力；引入 HTTP REST 之外的编排层；多 agent 协同。

## 2. 决策记录

| 决策 | 选择 | 理由 |
|---|---|---|
| 技术栈 | Go + `modelcontextprotocol/go-sdk` | 保留 `access` / `tools` / `llm` 约 5000 行已验证代码与 500+ 测试；交付是单个静态二进制，运维只需 scp 一个文件 |
| SDK 版本 | `v1.3.1` | 要求 Go 1.23；`v1.4.x` 要求 Go 1.25。本机 1.23.2。`v1.3.1` 已支持 `Server.AddTool` 与 `NewStreamableHTTPHandler`，满足需要 |
| 重写范围 | 整个仓库 | 旧产品线一并移除，不做并行共存 |
| 旧 K8s 产品线 | 删除 | 零测试、无消费方、与目标场景无关 |
| Agent 循环 | 内置与外部两套都保留 | 飞书机器人不能依赖外部平台；同时要让 Cursor/Dify 能用 |
| 知识事实源 | `skills/ceph-doctor/SKILL.md` | 与 aice 一致；Go 侧用 `go:embed` 读同一份 |
| 包可见性 | `internal/` | 纯 CLI，不应被外部 import |
| MCP 集群绑定 | 一进程一集群 | 避免在每个工具 schema 里加 `cluster` 参数 |
| 传输 | stdio（必须）+ Streamable HTTP | stdio 给 Cursor/Claude Code；HTTP 给 Dify |

## 3. 目录结构

借的是 aice 的**职责分层**，不是目录名。`members/`、`infra/workflows/` 等面向多仓库协作的目录对单仓库 Go CLI 没有意义。

```
infra-agent/
├── README.md
├── main.go                       # 只调 cmd.Execute()
│
├── cmd/                          # ① 薄入口：解析参数 → 调函数 → 打印
│   ├── root.go
│   ├── doctor.go                 #   内置 agent，交互式调查
│   ├── mcp.go                    #   MCP server（stdio / HTTP）
│   ├── serve.go                  #   飞书机器人
│   ├── selfcheck.go              #   在真实集群上验证工具（doctor --self-check）
│   └── version.go
│
├── internal/
│   ├── config/                   # ② 唯一配置层
│   │   ├── config.go             #   Config 结构体 + 默认值
│   │   └── load.go               #   flag/env/file 合并，只此一处
│   │
│   ├── access/                   # ③ 连接层
│   │   ├── cluster.go            #   SSH 链式接入、执行、超时
│   │   ├── policy*.go            #   只读命令白名单 / 拒绝规则
│   │   ├── probe.go              #   能力探测
│   │   ├── topology.go           #   拓扑发现
│   │   ├── output.go             #   语义化输出与错误分类
│   │   ├── quote.go              #   POSIX 引号
│   │   └── config.go             #   access.Config（连接参数，被 config 填充）
│   │
│   ├── ceph/
│   │   ├── tools/                # ④ 工具实现，唯一一份
│   │   │   ├── registry.go       #   Tool 接口 / Registry / Specs
│   │   │   ├── overview.go       #   新增 ceph_overview
│   │   │   └── <16 个现有工具>.go
│   │   ├── snapshot/             #   会话起始快照
│   │   │   ├── collect.go
│   │   │   ├── correlate.go
│   │   │   └── podhealth.go
│   │   └── status.go             #   ceph status 解析（snapshot 与 tools 共用）
│   │
│   ├── evidence/                 # ⑤ 证据链
│   │   └── chain.go
│   │
│   ├── agent/                    # ⑥ 内置调查循环
│   │   ├── agent.go              #   循环 / 预算 / 去重 / need_more_info
│   │   ├── prompt.go             #   SKILL.md + 动态集群段
│   │   ├── skill.go              #   read_skill 工具
│   │   └── llm/                  #   OpenAI 兼容客户端
│   │       ├── client.go
│   │       └── types.go
│   │
│   ├── session/                  # ⑦ 一套集群的运行时
│   │   └── session.go            #   Dial → 快照 → Registry → Agent
│   │
│   ├── mcpserver/                # ⑧ MCP 适配层
│   │   ├── server.go             #   Registry → mcp.Server；stdio
│   │   └── http.go               #   Streamable HTTP + Bearer
│   │
│   └── feishu/                   # ⑨ 机器人：消息收发 + 会话管理
│       ├── bot.go
│       └── session.go
│
├── skills/                       # ⑩ 诊断知识，唯一事实源
│   ├── embed.go
│   └── ceph-doctor/
│       ├── SKILL.md
│       └── references/
│           ├── osd.md
│           ├── pg.md
│           ├── performance.md
│           └── rook.md
│
├── config/
│   └── doctor.example.yaml       # 一份配置，三个命令共用
│
└── docs/
    ├── ARCHITECTURE.md
    ├── MCP.md
    └── GETTING_STARTED.md
```

### 3.1 依赖方向

严格单向，新增反向依赖即视为结构退化：

```
cmd ──→ config
 │
 ├──→ session ──→ agent ──→ ceph/tools ──→ ceph/snapshot ──→ ceph/status ──→ access
 │      │           │            │                │              │
 │      │           │            └────────────────┴──────────────┘
 │      │           └→ evidence
 │      └──→ ceph/snapshot ──→ evidence
 ├──→ mcpserver ──→ ceph/tools
 └──→ feishu ──→ session
```

约束：

- `ceph/tools` 不 import `agent`、`session`、`mcpserver`
- `ceph/snapshot` 不 import `agent`
- 任何包不 import `cmd`
- `access` 只 import 标准库 + `golang.org/x/crypto/ssh`

### 3.2 包职责边界

| 包 | 做什么 | 不做什么 |
|---|---|---|
| `cmd` | flag 定义、调用、输出 | 业务逻辑、配置解析细节 |
| `config` | 把 flag/env/file 合成一个 `Config` | 连接、调度 |
| `access` | 建立 SSH、执行命令、判定只读、探测能力、发现拓扑 | 理解 Ceph 语义 |
| `ceph/tools` | 执行一条查询、解析输出、生成 `Result` | 决定"什么时候查" |
| `ceph/snapshot` | 会话起点的一次性采集与确定性关联 | 调用模型 |
| `evidence` | 线程安全地记录工具调用 | 生成结论 |
| `agent` | 循环、预算、去重、prompt 组装 | 具体命令的解析 |
| `session` | 把上面几层装成一个可用会话 | 消息收发 |
| `mcpserver` | 把 Registry 暴露成 MCP 工具 | 参与调查决策 |
| `feishu` | 群消息 ↔ 会话 | 了解 Ceph |

## 4. 双消费、单实现

| 关注点 | 内置 agent（`doctor` / `serve`） | 外部 agent（MCP） |
|---|---|---|
| 工具执行 | `tools.Registry.Execute` | 同一 Registry，经 `mcpserver` 适配 |
| 领域知识 | `go:embed skills/ceph-doctor/**` 进 system prompt | 客户端读 `skills/ceph-doctor/SKILL.md` |
| 细节文档 | `read_skill("osd")` 读 embed 的 references | 直接打开 `references/*.md` |
| 起始数据 | 会话开始时 `snapshot.Collect` 写入 prompt | 调 `ceph_overview` 拿同一份结果 |
| 循环机制 | `agent.go` | 外部 agent 自己的 |

### 4.1 工具描述的中立性

**规则**：`Tool.Description()` 只描述"这个工具做什么、参数是什么、返回什么"。

现在混在描述里的内置方专用提示必须移走，例如 `ceph_status` 当前的描述末尾：

> 注意：会话开始时已自动采集过一次，结果在系统提示词的集群背景里；除非用户问的是当前状态是否已经变化，否则不必重复调用。

这类信息对 MCP 客户端不成立（它们没有"会话开始时已采集"这件事）。移入 `SKILL.md` 时必须改写成对两种消费者都成立的表述，或放进内置 agent 的动态 prompt 段。

这是"一份工具定义同时服务两边"的必要条件，也是本设计里最容易退化的地方，由第 7 节的漂移测试兜底。

### 4.2 新增工具 `ceph_overview`

内部把 `snapshot.Collect` + `correlate` 的确定性结果包装成一个工具：

```json
{
  "health": "HEALTH_WARN",
  "summary": "mon 3，OSD 186 总/184 up/186 in，PG 10241（异常 1110），容量 62.3%",
  "health_detail": "…ceph health detail 原文…",
  "pod_health": "…Rook pod 异常摘要…",
  "findings": [
    {"summary": "…", "why": "…", "next": "…"}
  ],
  "osd_placement": [
    {"osd": 44, "node": "cloud-hdd05-node05", "device": "/dev/sdb", "status": "down"}
  ],
  "evidence": [{"tool": "ceph_status", "id": "ev-001"}]
}
```

作用：让外部 agent 也能拿到内置 agent 的"起跑线"，而不是自己从零扫 `ceph_status` / `ceph_health` / `rook_pods` / `osd_tree` 再心算关联。

`ceph_overview` 只做**确定性**分析（正则与结构化字段），不做猜测。与 `correlate.go` 现有边界一致。

## 5. 配置层

### 5.1 现状问题

| 文件 | 问题 |
|---|---|
| `cmd/doctor.go` `doctorOptions` | 只是 flag 的寄存器，字段与 `resolvedDoctorOptions` 几乎完全重复 |
| `cmd/doctor_config.go` `resolvedDoctorOptions` | 手写 `changed()` / `flagString()` / `flagInt()` / `flagBool()` 闭包重新实现优先级 |
| `cmd/serve.go` `loadBotConfig` | 从 `doctor.ssh` / `doctor.llm` / `doctor.loop` 再解析一遍，与上一项重复 |
| `config/doctor.yaml` | 与 example 并存，且含明文 API key |

### 5.2 目标形状

```go
// internal/config
type Config struct {
    DefaultCluster string
    Clusters       map[string]Cluster

    LLM    LLM
    Loop   Loop
    Policy Policy
    Ceph   Ceph
    MCP    MCP
    Feishu Feishu
    Run    Run
}

type Cluster struct {
    Host           string
    Port           int
    User           string
    Password       string
    PrivateKeyPath string
    KnownHostsPath string
    CommandTimeout int // 秒
    ProbeTimeout   int // 秒
}
```

加载只有一个入口：

```go
func Load(cmd *cobra.Command) (Config, error)
```

- `viper.SetDefault(...)` 提供默认值
- `viper.BindPFlags(cmd.Flags())` 让 flag 自动覆盖
- `viper.SetEnvPrefix("INFRA_AGENT")` + `AutomaticEnv()` 让环境变量参与
- `viper.Unmarshal(&cfg)` 一次成型

删除所有手写的优先级闭包。

### 5.3 配置形状统一

`doctor.ssh` 与 `feishu.bot.clusters` 两处重复的连接定义合并为一处：

```yaml
default_cluster: stor-01
clusters:
  stor-01:
    host: 10.3.14.48
    port: 22
    user: root
    private_key: ~/.ssh/id_rsa
    known_hosts: ~/.ssh/known_hosts
    command_timeout: 30
    probe_timeout: 8
```

`doctor` 用 `--cluster`（或 `default_cluster`）选一套；`mcp` 同样；`serve` 用 `chat_clusters` 做群绑定。

### 5.4 失效项处理

| 项 | 处理 |
|---|---|
| `Profile` / `--profile` | 删除（上次 revert 残留，无消费方） |
| `MaxPerTool` / `--max-per-tool` | 删除（agent 循环里没有这个限制） |
| `DryRun` / `SelfCheck` / `Interactive` | 保留为配置项，但只对 `doctor` 生效；`mcp` 与 `serve` 忽略 |

## 6. MCP 层

### 6.1 工具暴露

```go
// internal/mcpserver
func New(reg *tools.Registry) *mcp.Server {
    srv := mcp.NewServer(&mcp.Implementation{Name: "infra-agent", Version: version}, nil)
    for _, spec := range reg.Specs() {
        srv.AddTool(&mcp.Tool{
            Name:        spec.Name,
            Description: spec.Description,
            InputSchema: spec.Schema, // 手写 schema 直接使用
        }, handlerFor(reg, spec.Name))
    }
    return srv
}
```

`Server.AddTool(t *Tool, h ToolHandler)` 接受原始 JSON Schema（`json.RawMessage` 或任意可 marshal 成 JSON object 的值），因此现有 16 个工具的 `Schema() json.RawMessage` 无需改动，也不必用泛型版 `AddTool[In, Out]` 重写。参数解包与错误到 `CallToolResult{IsError: true}` 的转换在 `handlerFor` 里统一处理。

### 6.2 传输

| 模式 | 启动 | 用途 |
|---|---|---|
| stdio | `infra-agent mcp` | Cursor / Claude Code / Conductor |
| Streamable HTTP | `infra-agent mcp --http 127.0.0.1:9100 --token <t>` | Dify 等需要 URL 的客户端 |

HTTP 约束：

- 默认监听 `127.0.0.1`，绑定非回环地址必须显式给 `--http`
- 未配置 `--token` 时拒绝启动——该服务等价于把集群只读权限放到网络上
- 鉴权用 Bearer 中间件包在 `NewStreamableHTTPHandler` 外层
- `GET /health` 返回 `{"status":"ok"}`，供探活

### 6.3 集群绑定

**一个 MCP 进程 = 一个集群。**

`--cluster` 从 `clusters` 里选，缺省用 `default_cluster`。多集群需要多开进程，各用各的端口 / stdio。

不在每个工具的 schema 里加 `cluster` 参数：那会让 17 个工具的描述和参数表全部变复杂，而多集群并发本来就需要独立的 SSH 连接池。

### 6.4 连接生命周期

启动时**不**连接。首次工具调用时惰性 `Dial`；失败返回 `IsError`，下次调用重新尝试。理由：MCP 客户端在集群暂时不可达时仍应能完成初始化并列出工具。

进程退出时 `Close`。

## 7. Skill 层

### 7.1 内容边界

`SKILL.md` 放**领域知识**——外部 agent 也能读懂的：

- 什么样的症状该先看哪个工具
- Ceph 各类告警的含义与常见根因
- 输出要求（中文、简短、点名对象、不写模板化报告）
- 只读边界

**不放**内置 agent 的循环机制（`need_more_info`、预算、一轮一次工具）。这些是 `agent.go` 的机制，外部 agent 不需要也不该看到。

### 7.2 引用方式

```go
// skills/embed.go
package skills

//go:embed ceph-doctor
var FS embed.FS
```

- 内置 agent：`prompt.go` 读 `FS` 里的 `SKILL.md` 作为 system prompt 主体，再追加动态段（本次集群的健康、快照、findings、证据 ID、环境事实）
- `read_skill` 工具：读 `FS` 里的 `references/*.md`，让内置 agent 按需展开细节
- 外部 agent：直接读磁盘上的 `skills/ceph-doctor/`

### 7.3 防漂移测试

"一份知识两处用"能成立的前提是两边不脱节。三组断言：

1. `SKILL.md` 与 `references/*.md` 中提到的每个工具名（`ceph_status`、`osd_tree` 等），必须存在于 registry
2. registry 里每个工具，必须至少在 `SKILL.md` 出现一次——否则外部 agent 不知道存在这个工具
3. `references` 中被引用的文件名必须真实存在

第 2 条在新增工具时会失败，这是有意的：新工具必须同时补充它的使用时机。

### 7.4 references 划分

| 文件 | 覆盖 |
|---|---|
| `osd.md` | OSD down / 慢心跳 / 磁盘故障 / `osd_tree` / `osd_detail` / `host_io` |
| `pg.md` | PG 降级 / peering / undersized / 容量 / `pool_info` / `pg_detail` |
| `performance.md` | slow ops / 延迟 / perf counters / host 负载 |
| `rook.md` | Rook pod 层 / daemon 日志 / `rook_pods` / `daemon_log` |

## 8. 内置 Agent 循环

逻辑不变，只改组织：

- 从 `pkg/doctor/agent.go` 搬到 `internal/agent/agent.go`，循环 / 预算 / 去重 / `need_more_info` 全部保留
- `agent_prompt.go` 的静态部分移入 `skills/ceph-doctor/SKILL.md`；`prompt.go` 只负责读 embed + 拼接动态段
- `pkg/doctor/loop` 改名 `internal/evidence`：这个包只保存证据链，循环早已在 `agent` 里，包名与内容不符

保留的行为细节（配套测试一并迁移）：

- 首轮只给 `need_more_info`，模型判断信息不足时自动打开全部工具
- 单轮工具调用上限，多出的返回"未执行"
- 相同工具 + 相同参数的调用直接返回"已执行过"
- 轮数用尽后要求模型基于已有信息作答

## 9. 删除清单

| 删除 | 原因 |
|---|---|
| `pkg/analyzer/**`、`pkg/ai/**`、`pkg/common/**`、`pkg/diagnosis/**`、`pkg/executor/**`、`pkg/kubernetes/**`、`pkg/notifier/**`、`pkg/patrol/**` | 旧 K8s 产品线，约 3000 行，零测试，无消费方 |
| `cmd/analyze.go`、`cmd/patrol.go`、`cmd/config.go` | 同上（`config` 子命令只管理已删除的 AI/通知配置） |
| `FINAL_SUMMARY.md`、`FINAL_DELIVERY.md`、`FINAL_COMPLETE_SUMMARY.md`、`PROJECT_SUMMARY.md` | 交付残留 |
| `config/doctor.yaml` | 与 example 重复，且提交了明文 API key |
| `demo.sh`、`test.sh`、`Makefile.test` | 全部指向 `analyze` / `patrol` |
| `Makefile` 中 `analyze` / `patrol` / `test-analyze` 目标 | 同上；保留 build/test/fmt/vet 相关目标 |
| `snapshotRunner`（`cmd/doctor.go`） | 与 `ClusterRunner` 重复，零引用 |
| `CollectSnapshot(ctx, *Cluster, _ string)` | 第二个参数被丢弃 |
| `var _ = access.Capabilities{}`（`agent_prompt.go`） | 仅为占用 import |
| `var _ = time.Second`（`snapshot_test.go`） | 同上 |
| `turnStart`（`agent.go`） | 计算后立即丢弃 |
| `Profile`、`MaxPerTool` | 解析了从不使用 |
| `docs/AUTO_DIAGNOSIS.md`、`AUTONOMOUS_DIAGNOSIS.md`、`MULTI_CLUSTER_ARCHITECTURE.md`、`ROOK_CEPH_MONITORING.md`、`REMOTE_STORAGE_MONITORING.md` | 描述的能力已不存在 |
| `docs/ARCHITECTURE.md`、`docs/FEISHU_INTEGRATION.md`、`docs/GETTING_STARTED.md` | 重写 |

**安全项（独立于本次重做）**：`config/doctor.yaml` 中提交的 API key 必须在提交本轮改动前吊销并重新签发。删除文件不会使它失效——它已经在 git 历史里。

## 10. 测试策略

| 层级 | 内容 |
|---|---|
| `access`、`ceph/tools`、`agent/llm` | 现有 500+ 测试随包迁移，逻辑不变，只改 import 路径 |
| `config` | 优先级矩阵：flag 覆盖 file、env 覆盖 file、默认值兜底、`clusters` 选择与缺省 |
| `mcpserver` | 用 SDK 的 `InMemoryTransport` 起真 client：`ListTools` 断言 17 个工具与 schema 合法；逐个 `CallTool` 断言成功路径与错误路径（`IsError`）；HTTP 模式测 Bearer 缺失被拒 |
| `skills` | 三组漂移断言（7.3） |
| `agent` | 现有测试保留；新增：system prompt 首段来自 embed 的 `SKILL.md` |
| `ceph/snapshot` | 现有测试保留；新增 `correlate` 到 `ceph_overview` 输出形状的断言 |
| `feishu` | 现有测试保留 |

每步结束时 `go build ./... && go vet ./... && go test ./...` 必须通过。

## 11. 迁移顺序

每步一个 commit，可独立回滚：

1. 建分支；`git mv` 搬包到 `internal/`，改 import；删 `snapshotRunner` 等死代码。测试绿
2. 删除旧产品线与交付残留文档。测试绿
3. 引入 `internal/config`，`doctor` / `serve` / `selfcheck` 改用它；删除三套解析。配置优先级测试绿
4. 拆 `ceph/snapshot`，抽出 `ceph/status`，新增 `ceph_overview` 工具。测试绿
5. 写 `skills/ceph-doctor/**`；接 `go:embed`、`read_skill`；重写 `prompt.go`。加入漂移测试
6. 加 `mcpserver` + `cmd/mcp.go`（stdio）；补 `InMemoryTransport` 测试
7. 加 HTTP 传输与 Bearer 鉴权；补测试
8. 飞书改用 `internal/session`
9. 重写 README / ARCHITECTURE / MCP / GETTING_STARTED / `doctor.example.yaml`

## 12. 验收标准

1. `go build ./...`、`go vet ./...`、`go test ./...` 全通过
2. `infra-agent --help` 只列出 `doctor`、`mcp`、`serve`、`version`，无 `analyze` / `patrol` / `config`
3. 仓库无 `pkg/` 目录；`internal/` 之外只有 `cmd`、`skills`、`config`、`docs`
4. `grep -rn "Profile\|MaxPerTool\|snapshotRunner" --include="*.go"` 为空
5. `skills/ceph-doctor/SKILL.md` 提到的工具名与 registry 双向一致（漂移测试通过）
6. MCP stdio 模式下，客户端 `ListTools` 得到 17 个工具，`ceph_overview` 可调用
7. MCP HTTP 模式无 token 时拒绝启动；带 token 时可正常调用
8. `infra-agent doctor --dry-run` 与 `doctor --self-check` 在新结构下行为与现在一致
9. 旧产品线的 `analyze` / `patrol` 命令不存在，且无残留源码
10. `config/` 下只有 `doctor.example.yaml`，无明文密钥