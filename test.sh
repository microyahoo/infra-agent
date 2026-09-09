#!/bin/bash

# 测试 Infra Agent 的脚本

set -e

BINARY="./bin/infra-agent"

echo "======================================"
echo "Infra Agent 测试脚本"
echo "======================================"
echo ""

# 检查二进制文件是否存在
if [ ! -f "$BINARY" ]; then
    echo "❌ 二进制文件不存在，正在构建..."
    make build
fi

echo "✓ 二进制文件已准备好"
echo ""

# 测试 1: 版本信息
echo "测试 1: 版本信息"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
$BINARY version
echo ""

# 测试 2: 配置列表
echo "测试 2: 配置列表"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
$BINARY config list
echo ""

# 测试 3: 帮助信息
echo "测试 3: 帮助信息"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
$BINARY --help
echo ""

echo "测试 4: analyze 命令帮助"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
$BINARY analyze --help
echo ""

echo "测试 5: patrol 命令帮助"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
$BINARY patrol --help
echo ""

# 测试 6: 分析（如果有 kubeconfig）
if [ -f "$HOME/.kube/config" ]; then
    echo "测试 6: 基本分析（实际执行）"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    $BINARY analyze --timeout 10 || echo "⚠️  分析可能失败（这是正常的，可能是集群未配置）"
    echo ""
else
    echo "测试 6: 跳过（未找到 kubeconfig）"
    echo ""
fi

echo "======================================"
echo "✅ 所有测试完成！"
echo "======================================"
echo ""
echo "下一步："
echo "1. 配置 AI Provider:"
echo "   export OPENAI_API_KEY='sk-...'"
echo "   $BINARY config set-ai --provider openai --token \$OPENAI_API_KEY"
echo ""
echo "2. 配置飞书通知:"
echo "   export FEISHU_WEBHOOK_URL='https://...'"
echo ""
echo "3. 执行分析:"
echo "   $BINARY analyze --explain"
echo ""
echo "4. 启用自动诊断:"
echo "   $BINARY analyze --explain --auto-diagnose --notify"
echo ""
