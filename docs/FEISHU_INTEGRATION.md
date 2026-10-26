# 飞书集成指南

## 配置飞书机器人

### 1. 创建自定义机器人

1. 在飞书群聊中，点击右上角的设置按钮
2. 选择「群机器人」→「添加机器人」→「自定义机器人」
3. 设置机器人名称和描述，例如：
   - 名称：`Infra Agent`
   - 描述：`基础设施监控和自动诊断机器人`
4. 点击「添加」

### 2. 获取 Webhook 地址

创建后，您会获得一个 Webhook 地址，格式如下：

```
https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
```

### 3. 配置签名验证（可选但推荐）

在机器人设置中，可以启用「签名校验」：

1. 勾选「签名校验」
2. 记录生成的密钥（Secret）
3. 保存配置

## 配置 Infra Agent

### 方法 1: 环境变量

```bash
export FEISHU_WEBHOOK_URL="https://open.feishu.cn/open-apis/bot/v2/hook/YOUR_WEBHOOK_KEY"
export FEISHU_SECRET="your_secret_key"  # 如果启用了签名验证
```

### 方法 2: 配置文件

编辑 `~/.infra-agent.yaml`:

```yaml
notifiers:
  default: "feishu"
  feishu:
    enabled: true
    webhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/YOUR_WEBHOOK_KEY"
    secret: "your_secret_key"  # 可选
```

### 方法 3: 命令行配置

```bash
# 使用 config 命令（TODO: 需要实现）
infra-agent config set notifier.feishu.webhookURL "YOUR_WEBHOOK_URL"
infra-agent config set notifier.feishu.secret "YOUR_SECRET"
```

## 使用示例

### 发送分析结果

```bash
# 分析并发送通知
infra-agent analyze --explain --notify

# 带自动诊断的完整流程
infra-agent analyze --explain --auto-diagnose --notify
```

### 发送巡检报告

```bash
# 执行巡检并发送报告
infra-agent patrol run --notify
```

## 消息格式

### 1. 诊断结果卡片

当发现问题并完成诊断后，会发送包含以下内容的卡片：

```
🚨 自动诊断报告
━━━━━━━━━━━━━━━━━━━━━━━━━━━
资源: default/my-app (Pod)
严重程度: 🔴 critical

🔍 问题描述
• Container app is in CrashLoopBackOff
• Container app has high restart count: 10

🤖 AI 分析
根据日志和诊断信息...
[详细的根因分析]

📋 诊断详情
执行的命令:
- check-process: 进程检查
- check-memory: 内存检查
[结果摘要]

💡 解决方案
1. 调整内存限制...
2. 优化配置...
3. 重启服务...
```

### 2. 巡检报告卡片

定期巡检完成后发送的报告：

```
🔍 基础设施巡检报告 - Warning
━━━━━━━━━━━━━━━━━━━━━━━━━━━

📊 巡检摘要
总问题数: 5 | 严重: 1 | 高: 2 | 中: 1 | 低: 1

开始时间: 2024-01-01 10:00:00
耗时: 45s

🔍 发现的问题

🔴 default/db-pod (Pod)
  • Container is in CrashLoopBackOff
  💡 解决方案: 检查配置文件...

🟠 default/cache-pvc (PVC)
  • PVC is in Pending state
  💡 解决方案: 检查 StorageClass...

... 还有 3 个问题
```

## 消息样式

### 严重程度颜色

- 🔴 Critical (红色卡片头)
- 🟠 High (橙色卡片头)
- 🟡 Medium (黄色卡片头)
- 🟢 Low (绿色卡片头)

### 状态颜色

- ✅ OK (绿色)
- ⚠️ Warning (橙色)
- ❌ Error (红色)

## 高级功能

### @ 提醒特定用户

在配置中添加需要提醒的用户：

```yaml
notifiers:
  feishu:
    atUsers:
      - "user_id_1"
      - "user_id_2"
```

### 关键词告警

飞书支持配置关键词，当消息包含特定关键词时触发特殊提醒：

1. 在机器人设置中添加关键词
2. Agent 会自动在严重问题的消息中包含关键词

### 消息频率控制

避免告警风暴：

```yaml
notifiers:
  feishu:
    # 相同问题的最小通知间隔（分钟）
    minInterval: 30
    # 每小时最大消息数
    maxMessagesPerHour: 10
```

## 故障排查

### 1. 消息发送失败

**错误信息：**
```
feishu error: invalid webhook url (code: 19001)
```

**解决方法：**
- 检查 Webhook URL 是否正确
- 确认机器人未被删除或禁用
- 验证网络连接

### 2. 签名验证失败

**错误信息：**
```
feishu error: sign not match (code: 19021)
```

**解决方法：**
- 检查 Secret 配置是否正确
- 确认系统时间同步
- 验证签名算法实现

### 3. 消息发送过于频繁

**错误信息：**
```
feishu error: send message too fast (code: 19024)
```

**解决方法：**
- 增加消息发送间隔
- 合并相似的告警
- 启用消息聚合功能

### 4. 无法显示卡片

**症状：** 收到纯文本消息而不是卡片

**解决方法：**
- 检查卡片 JSON 格式是否正确
- 确认使用的是最新版本的卡片格式
- 查看飞书开放平台文档更新

## 测试

### 测试消息发送

```bash
# 发送测试消息
curl -X POST "$FEISHU_WEBHOOK_URL" \
  -H "Content-Type: application/json" \
  -d '{
    "msg_type": "text",
    "content": {
      "text": "这是一条测试消息"
    }
  }'
```

### 测试 Agent 通知

```bash
# 创建一个有问题的 Pod 进行测试
kubectl run test-pod --image=invalid-image

# 运行分析并发送通知
infra-agent analyze --explain --notify -n default

# 清理
kubectl delete pod test-pod
```

## API 限制

飞书机器人 Webhook 的限制：

- **频率限制**: 每分钟最多 20 条消息
- **消息大小**: 单条消息最大 20KB
- **卡片复杂度**: 建议不超过 10 个元素块

## 最佳实践

1. **启用签名验证** - 提高安全性
2. **合理设置通知频率** - 避免告警疲劳
3. **使用卡片消息** - 更好的可读性
4. **关键问题优先** - 只通知 high/critical 级别
5. **添加快捷操作** - 在卡片中添加常用命令按钮（TODO）
6. **定期检查机器人** - 确保机器人未被移除

## 相关文档

- [飞书开放平台 - 自定义机器人](https://open.feishu.cn/document/ukTMukTMukTM/ucTM5YjL3ETO24yNxkjN)
- [飞书卡片消息指南](https://open.feishu.cn/document/ukTMukTMukTM/uczM3QjL3MzN04yNzcDN)
- [飞书消息卡片搭建工具](https://open.feishu.cn/tool/cardbuilder)
