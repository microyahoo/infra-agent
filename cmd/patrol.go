package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/microyahoo/infra-agent/pkg/analyzer"
	"github.com/microyahoo/infra-agent/pkg/analyzer/k8s"
	"github.com/microyahoo/infra-agent/pkg/analyzer/storage"
	"github.com/microyahoo/infra-agent/pkg/common"
	"github.com/microyahoo/infra-agent/pkg/kubernetes"
	"github.com/microyahoo/infra-agent/pkg/notifier/feishu"
	"github.com/microyahoo/infra-agent/pkg/patrol"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	patrolInterval string
	patrolOnce     bool
)

// patrolCmd represents the patrol command
var patrolCmd = &cobra.Command{
	Use:   "patrol",
	Short: "启动或管理巡检服务",
	Long: `启动定期巡检服务，持续监控集群健康状态。

巡检调度器会按照配置的时间间隔自动执行分析，发现问题后
自动进行诊断，并将结果发送到飞书。

示例:
  # 启动巡检服务，每 30 分钟执行一次
  infra-agent patrol start --interval 30m

  # 执行一次巡检
  infra-agent patrol run

  # 查看最后一次巡检报告
  infra-agent patrol report
`,
}

// patrolStartCmd represents the patrol start command
var patrolStartCmd = &cobra.Command{
	Use:   "start",
	Short: "启动巡检服务",
	Long:  `启动巡检调度服务，按照指定的时间间隔自动执行巡检。`,
	RunE:  runPatrolStart,
}

// patrolRunCmd represents the patrol run command
var patrolRunCmd = &cobra.Command{
	Use:   "run",
	Short: "执行一次巡检",
	Long:  `立即执行一次巡检，不启动调度服务。`,
	RunE:  runPatrolRun,
}

// patrolReportCmd represents the patrol report command
var patrolReportCmd = &cobra.Command{
	Use:   "report",
	Short: "查看最后一次巡检报告",
	RunE:  runPatrolReport,
}

func init() {
	rootCmd.AddCommand(patrolCmd)
	patrolCmd.AddCommand(patrolStartCmd)
	patrolCmd.AddCommand(patrolRunCmd)
	patrolCmd.AddCommand(patrolReportCmd)

	patrolStartCmd.Flags().StringVar(&patrolInterval, "interval", "30m", "巡检间隔时间 (例如: 5m, 1h, 30m)")
}

func runPatrolStart(cmd *cobra.Command, args []string) error {
	// 解析间隔时间
	interval, err := time.ParseDuration(patrolInterval)
	if err != nil {
		return fmt.Errorf("invalid interval: %w", err)
	}

	// 初始化组件
	scheduler, err := initPatrolScheduler(interval)
	if err != nil {
		return err
	}

	// 启动巡检调度
	return scheduler.Start()
}

func runPatrolRun(cmd *cobra.Command, args []string) error {
	// 使用一个较大的间隔，因为只执行一次
	scheduler, err := initPatrolScheduler(24 * time.Hour)
	if err != nil {
		return err
	}

	// 执行一次巡检后获取报告
	go func() {
		time.Sleep(2 * time.Second)
		scheduler.Stop()
	}()

	return scheduler.Start()
}

func runPatrolReport(cmd *cobra.Command, args []string) error {
	// 这里应该从存储中读取最后一次报告
	// 暂时返回提示信息
	fmt.Println("巡检报告功能正在开发中...")
	fmt.Println("提示: 使用 'infra-agent patrol run' 执行一次巡检")
	return nil
}

func initPatrolScheduler(interval time.Duration) (*patrol.Scheduler, error) {
	// 创建 Kubernetes 客户端
	client, restConfig, err := kubernetes.NewClientWithConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// 创建分析器注册表
	registry := analyzer.NewAnalyzerRegistry()

	// 注册所有分析器
	registry.Register(k8s.NewPodAnalyzer())
	registry.Register(k8s.NewDeploymentAnalyzer())
	registry.Register(k8s.NewServiceAnalyzer())
	registry.Register(k8s.NewNodeAnalyzer())
	registry.Register(k8s.NewStatefulSetAnalyzer())
	registry.Register(storage.NewPVCAnalyzer())

	// 获取 AI Provider (使用 analyze.go 中的共享函数)
	aiProvider, err := getAIProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ 警告: 无法初始化 AI Provider: %v\n", err)
		fmt.Fprintf(os.Stderr, "⚠ 将跳过 AI 诊断功能\n")
		aiProvider = nil
	}

	// 获取通知器
	var notifier common.Notifier
	webhookURL := viper.GetString("notifiers.feishu.webhookURL")
	if webhookURL != "" {
		secret := viper.GetString("notifiers.feishu.secret")
		notifier = feishu.NewFeishuNotifier(webhookURL, secret)
		fmt.Fprintf(os.Stderr, "✓ 飞书通知已配置\n")
	} else {
		fmt.Fprintf(os.Stderr, "⚠ 警告: 未配置飞书通知\n")
	}

	// 创建巡检调度器
	scheduler := patrol.NewScheduler(
		client,
		restConfig,
		registry,
		aiProvider,
		notifier,
		interval,
	)

	return scheduler, nil
}
