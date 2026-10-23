# 回放基线 (Replay Fixtures)

本目录存放 **会话录制**，用于在开发与 CI 中离线回放真实集群的命令输出，
取代对真实集群的依赖。

## 目录布局

```
testdata/replay/
├── README.md
└── <cluster-id>/
    ├── session.jsonl   # 每次远程执行一行 JSON（必需）
    └── meta.json       # 集群元信息（可选，见下）
```

- `<cluster-id>` 建议使用集群 fsid 或稳定别名（如 `prod-ceph-a`），一个目录对应一个集群。
- `session.jsonl` 每行一条记录：

  ```json
  {"node":"node-3","argv":["ceph","-s"],"stdout":"cluster: id: ...","stderr":"","exit_code":0,"duration":123456789}
  ```

  字段：`node`（节点名）、`argv`（原始命令参数）、`stdout`、`stderr`、
  `exit_code`、`duration`（time.Duration，纳秒）、`error`、`error_kind`（失败时的错误
  文本与语义分类：`timeout` / `unreachable` / `unavailable` / `failed`）。

- `meta.json` 可选，用于自描述（便于人工审阅，回放本身不读取）：

  ```json
  {
    "cluster_id": "prod-ceph-a",
    "fsid": "b1c2d3...",
    "ceph_version": "18.2.0",
    "recorded_at": "2026-10-23T10:00:00+08:00",
    "recorded_by": "infra-agent doctor --record",
    "notes": "慢请求排查基线"
  }
  ```

## 如何录制

在能够访问目标集群的环境中以录制模式运行 doctor：

```bash
infra-agent doctor --cluster prod-ceph-a --host node-1 --record --out testdata/replay/prod-ceph-a
```

录制完成后会得到 `testdata/replay/prod-ceph-a/session.jsonl`，
建议手工补一份 `meta.json` 并做一次敏感信息检查（见下文）。

## 用途

- **离线回归**：40 个集群各留一份录制，即为 40 个永久测试夹具。
  工具层、证据链、报告渲染的任何改动都可以在这些录制上快速验证，
  无需重新连集群、也不受集群当前状态漂移影响。
- **复现问题**：用户报告的异常可随 session.jsonl 一起提交，问题现场得以完整保留。
- **确定性**：回放不重新截断、不重新分类错误，同一 node+argv 始终返回录制时的
  Output 与 `*ToolError`；未录制的调用会明确报错（包含「未录制」），
  而不是返回空输出导致结论失真。

## 敏感信息警告

录制内容是 **真实集群的原始输出**，可能包含：

- 节点主机名、内网 IP / 公网 IP
- ceph 配置片段、auth key、token 等凭据
- 业务 pool / image / 路径名称

**提交前务必检查**：

```bash
# 扫描常见敏感模式
grep -nEi 'BEGIN [A-Z ]*PRIVATE KEY|AQ[A-Za-z0-9+/=]{20,}|([0-9]{1,3}\.){3}[0-9]{1,3}' \
  testdata/replay/<cluster-id>/session.jsonl
```

发现敏感内容时，按需替换为等长的占位符（例如把 IP 改为 `10.0.0.x`），
但不要改变 JSON 结构与长度以外的逻辑，避免破坏回放的一致性。