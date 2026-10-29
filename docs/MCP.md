# MCP 接入

## stdio 模式（Cursor / Claude Code）

在客户端配置里添加：

```json
{
  "mcpServers": {
    "infra-agent": {
      "command": "/path/to/infra-agent",
      "args": ["mcp", "--cluster", "stor-01"]
    }
  }
}
```

进程按需启动，不需要常驻。`tools/list` 不触发 SSH 连接；第一次 `tools/call` 才建立连接。

## HTTP 模式（Dify / 其他需要 URL 的客户端）

```bash
infra-agent mcp --cluster stor-01 --http 127.0.0.1:9100 --token "$INFRA_AGENT_MCP_TOKEN"
```

验证服务是否就绪：

```bash
curl -s http://127.0.0.1:9100/health
# {"status":"ok"}
```

## 工具清单（17 个）

| 工具 | 用途 |
|---|---|
| `ceph_overview` | 整体健康、告警、Rook pod 异常、确定性关联结论 |
| `ceph_health` | 健康码与告警明细原文 |
| `ceph_status` | 集群计数、容量、PG 分布 |
| `osd_tree` | OSD 拓扑，哪个 OSD 在哪台机器 |
| `osd_detail` | 单个 OSD 的状态与设备 |
| `pool_info` | 池的容量、副本、使用率 |
| `pg_detail` | PG 状态分布与异常 PG |
| `slow_ops` | 慢请求、阻塞请求 |
| `perf_counters` | OSD 内部计数器（延迟、队列） |
| `host_io` | 宿主机磁盘 I/O、SMART |
| `host_check` | 宿主机负载、内存、网络 |
| `rook_pods` | Rook 组件 pod 状态 |
| `daemon_log` | 某个 daemon 的日志 |
| `read_log` | 任意日志文件 |
| `explore` | 目录浏览与按模式查找 |
| `run_command` | 受只读策略约束的任意命令 |
| `ceph_source_lookup` | 按版本查 Ceph 源码（需配置 ceph.source_dir） |

## 安全说明

- **全部只读**：变更类命令在 `internal/access` 层被拒绝，外部 agent 没有绕过手段
- **Bearer token 必填**：HTTP 模式不设 token 拒绝启动
- **默认回环**：HTTP 模式默认只绑定 127.0.0.1；绑定其他地址需显式 `--allow-remote`
- **一进程一集群**：多集群请多开进程

## 如何让外部 agent 读到诊断知识

把 `skills/ceph-doctor/` 目录挂载到外部 agent 能读到的位置，或者直接把 `SKILL.md` 的内容粘贴到系统提示里。外部 agent 与内置 agent 读到的内容完全一致。
