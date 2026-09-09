#!/bin/bash

# Infra Agent 演示脚本

set -e

echo "╔══════════════════════════════════════════════════════════╗"
echo "║     Infra Agent - 智能基础设施运维 Agent 演示           ║"
echo "╚══════════════════════════════════════════════════════════╝"
echo ""

BINARY="./bin/infra-agent"

# 颜色定义
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${BLUE}▶ 检查二进制文件...${NC}"
if [ ! -f "$BINARY" ]; then
    echo "  构建中..."
    make build
fi
echo -e "${GREEN}  ✓ 二进制文件就绪${NC}"
echo ""

echo -e "${BLUE}▶ 版本信息${NC}"
$BINARY version
echo ""

echo -e "${BLUE}▶ 功能列表${NC}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

echo -e "${YELLOW}1. analyze${NC} - 智能分析"
echo "   基本用法: infra-agent analyze"
echo "   AI 诊断:  infra-agent analyze --explain --auto-diagnose --notify"
echo ""

echo -e "${YELLOW}2. patrol${NC} - 自动巡检"
echo "   启动巡检: infra-agent patrol start --interval 30m"
echo "   单次巡检: infra-agent patrol run"
echo ""

echo -e "${YELLOW}3. config${NC} - 配置管理"
echo "   查看配置: infra-agent config list"
echo "   设置 AI:  infra-agent config set-ai --provider openai --token 'sk-...'"
echo ""

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

echo -e "${BLUE}▶ 核心特性${NC}"
echo ""
echo "  🤖 智能诊断     - AI 双阶段分析，自动决策诊断方向"
echo "  🔧 远程执行     - 在 Pod 中自动执行诊断命令"
echo "  📊 多维度分析   - Pod、Deployment、Service、Node、PVC"
echo "  🔔 飞书告警     - 精美的卡片通知，实时推送"
echo "  🔍 自动巡检     - 定期检查，持续监控"
echo "  📝 详细文档     - 5 份完整的使用指南"
echo ""

echo -e "${BLUE}▶ 项目统计${NC}"
echo ""
echo "  代码行数: ~3,700+ 行"
echo "  Go 文件:  $(find . -name "*.go" | wc -l) 个"
echo "  分析器:   6 个"
echo "  文档:     5 份"
echo ""

echo -e "${BLUE}▶ 快速开始${NC}"
echo ""
echo "  1. 配置 AI Provider:"
echo "     export OPENAI_API_KEY='sk-...'"
echo "     $BINARY config set-ai --provider openai --token \$OPENAI_API_KEY"
echo ""
echo "  2. 配置飞书通知:"
echo "     export FEISHU_WEBHOOK_URL='https://...'"
echo ""
echo "  3. 开始使用:"
echo "     $BINARY analyze --explain --auto-diagnose --notify"
echo ""

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo -e "${GREEN}✨ 项目已完成！所有核心功能已实现！${NC}"
echo ""
echo "📚 查看文档:"
echo "   - docs/GETTING_STARTED.md  - 快速开始"
echo "   - docs/ARCHITECTURE.md     - 架构设计"
echo "   - docs/AUTO_DIAGNOSIS.md   - 自动诊断"
echo "   - PROJECT_SUMMARY.md       - 项目总结"
echo ""
