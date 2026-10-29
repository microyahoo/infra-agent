# infra-agent

通过 SSH 只读诊断 Ceph / Rook 集群。同一套工具可以三种方式使用：

| 形态 | 命令 | 适用 |
|---|---|---|
| 命令行 | `infra-agent doctor` | 人工排查、追问 |
| MCP server | `infra-agent mcp` | Cursor / Claude Code / Dify |
| 飞书机器人 | `infra-agent serve` | 群里 @ 提问 |

## 它是什么

- **只读**：变更类命令在执行层被拒绝，模型没有绕过手段
- **证据可追溯**：每次工具调用都记录命令、输出与耗时
- **知识只有一份**：`skills/ceph-doctor/SKILL.md`，内置 agent 与外部 agent 共用

## 快速开始

见 [docs/GETTING_STARTED.md](docs/GETTING_STARTED.md)。

## 文档

- [架构](docs/ARCHITECTURE.md)
- [MCP 接入](docs/MCP.md)
- [配置示例](config/doctor.example.yaml)

## 目录

| 路径 | 职责 |
|---|---|
| `cmd/` | cobra 命令入口，只做参数解析与输出 |
| `internal/access/` | SSH 连接、只读策略、能力探测、拓扑发现 |
| `internal/ceph/tools/` | 17 个只读诊断工具 + 工具注册表 |
| `internal/ceph/snapshot/` | 会话起点快照采集与确定性告警关联 |
| `internal/agent/` | 内置对话调查循环 |
| `internal/mcpserver/` | 把工具表转成 MCP 的薄适配层 |
| `internal/session/` | 一套集群的运行时装配（SSH + 工具表 + 快照 + agent） |
| `internal/feishu/` | 飞书长连接机器人 |
| `internal/config/` | 统一配置层 |
| `skills/ceph-doctor/` | Ceph 诊断知识主文档与细节参考 |

## 开发

```bash
make build   # 编译
make test    # 测试
```
