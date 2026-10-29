# 快速开始

## 1. 编译

```bash
git clone https://github.com/microyahoo/infra-agent
cd infra-agent
make build
# 或直接 go build -o bin/infra-agent .
```

## 2. 写配置

复制示例配置并填写集群信息：

```bash
cp config/doctor.example.yaml ~/.infra-agent.yaml
```

最少需要填写：

```yaml
default_cluster: stor-01

clusters:
  stor-01:
    host: 10.3.14.48
    user: root
    private_key: ~/.ssh/id_rsa

llm:
  model: gpt-4o
  # 建议用环境变量，不要写进文件
  # api_key: ""
```

API Key 用环境变量提供：

```bash
export INFRA_AGENT_LLM_API_KEY=sk-...
# 或者如果你的 key 在 OPENAI_API_KEY：
export OPENAI_API_KEY=sk-...
```

## 3. 验证 SSH 连接

```bash
infra-agent doctor --cluster stor-01 --dry-run
```

预期输出：
```
SSH 接入验证（dry-run）
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
入口节点: 10.3.14.48
诊断后端: ceph
ceph CLI: 可用
...
dry-run 结束：SSH 与探测已完成，未调用模型
```

## 4. 验证诊断工具

```bash
infra-agent doctor --cluster stor-01 --self-check
```

这会在真实集群上逐个运行全部 17 个工具，确认它们都能正常取到数据。工具失败会在这里暴露，而不是等到模型调用时才发现。

## 5. 开始提问

```bash
infra-agent doctor --cluster stor-01
```

进入交互模式后，直接说你想查什么：

```
❯ 集群为什么 warn 了
❯ osd.13 这块盘正常吗
❯ 最近写入延迟高不高
```

## MCP 最小配置

在 Cursor / Claude Code 里添加：

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

详见 [docs/MCP.md](MCP.md)。

## 飞书机器人最小配置

在 `~/.infra-agent.yaml` 里增加：

```yaml
feishu:
  app_id: cli_...
  app_secret: ...
  default_cluster: stor-01
```

启动：

```bash
infra-agent serve
```

## 常见错误

**API Key 未设置**
```
未找到 API Key：请设置环境变量 OPENAI_API_KEY，或在配置文件 llm.api_key 中填写
```
解决：`export OPENAI_API_KEY=sk-...`

**known_hosts 不存在**
```
open /root/.ssh/known_hosts: no such file or directory
```
解决：先 `ssh-keyscan <host> >> ~/.ssh/known_hosts`，或者在配置里留空 `known_hosts`（退回 TOFU 模式）。

**集群只配了 host 没配认证**
```
接入失败: dial: no authentication method
```
解决：配置 `private_key` 或 `password`。

**连接超时**
```
接入失败: dial: ssh: handshake failed
```
解决：确认 host / port / user 正确，检查 SSH 服务是否可达。
