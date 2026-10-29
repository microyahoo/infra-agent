# 架构

## 数据流

```
cmd (doctor/mcp/serve)
   │
   ├─ doctor/serve ─→ session ─→ agent ─→ ceph/tools ─→ access ─→ SSH
   │                     │          │
   │                     │          └→ evidence
   │                     └→ ceph/snapshot ─→ access ─→ SSH
   └─ mcp ─→ mcpserver ─→ ceph/tools ─→ access ─→ SSH
```

## 分层规则

1. `cmd` 只做参数解析与输出，不包含任何业务逻辑
2. `internal/config` 是唯一配置层，其他包只接受已解析的结构体
3. `internal/access` 管理所有 SSH 连接与只读策略，是安全边界
4. `internal/ceph/tools` 是唯一工具实现，doctor / MCP / serve 都用同一套
5. `internal/session` 是装配点，负责把连接、工具表、快照、agent 组合成一次可用会话
6. `internal/agent` 是内置调查循环，只有 doctor / serve 使用
7. `internal/mcpserver` 是把工具表转成 MCP 协议的薄适配层
8. `skills/ceph-doctor/` 是唯一诊断知识源

## 工具唯一、知识唯一、循环可两份

- **工具唯一**：`internal/ceph/tools` 里的 17 个工具同时服务于内置 agent 和 MCP server。两条路径不能有各自的工具实现。
- **知识唯一**：`skills/ceph-doctor/SKILL.md` 是唯一的诊断知识来源。内置 agent 通过 `go:embed` 读取它；外部 agent（Cursor / Claude）直接读磁盘上的文件——两者看到的内容完全一致。
- **循环可两份**：调查循环（`internal/agent`）是内置 agent 专属的，MCP 客户端有自己的推理循环，因此两份循环是正确的设计。

## 只读策略的执行点

只读策略在 `internal/access` 层执行，具体在 `cluster.go` 的 `Run()` 方法里。在命令被发送到远端之前，它会检查命令是否在只读允许列表中。这意味着：

- 工具层无法绕过只读检查
- 模型生成的任意命令都会被过滤
- `--extra-binaries` 可以扩展允许列表，但不能移除内置的禁止项

## 证据链

`internal/evidence` 维护一条不可变的证据链，每条证据包含：
- 工具名、参数、命令
- 节点、原始输出、错误码、耗时
- 是否被截断

证据链有三个用途：
1. 让模型看到"之前查过什么"，避免重复查询
2. 提供给用户的 `:evidence` 命令，便于审计
3. 作为诊断报告的可引用依据
