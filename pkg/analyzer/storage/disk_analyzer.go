package storage

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/analyzer"
	"github.com/microyahoo/infra-agent/pkg/common"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// DiskAnalyzer 磁盘分析器
type DiskAnalyzer struct {
	analyzer.BaseAnalyzer
}

// NewDiskAnalyzer 创建磁盘分析器
func NewDiskAnalyzer() *DiskAnalyzer {
	return &DiskAnalyzer{
		BaseAnalyzer: analyzer.BaseAnalyzer{
			Name:    "DiskAnalyzer",
			Kind:    "Disk",
			Enabled: true,
		},
	}
}

// Analyze 执行磁盘分析
func (a *DiskAnalyzer) Analyze(ctx context.Context, client *kubernetes.Clientset, namespace string) ([]common.Result, error) {
	var results []common.Result

	// 获取所有节点
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	for _, node := range nodes.Items {
		if errs := a.analyzeNodeDisk(&node); len(errs) > 0 {
			results = append(results, common.Result{
				Kind:      "Disk",
				Name:      node.Name,
				Namespace: "",
				Error:     errs,
				Details:   "Node disk analysis",
			})
		}
	}

	return results, nil
}

// analyzeNodeDisk 分析节点磁盘
func (a *DiskAnalyzer) analyzeNodeDisk(node *v1.Node) []common.Error {
	var errors []common.Error

	// 检查磁盘压力条件
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeDiskPressure && condition.Status == v1.ConditionTrue {
			errors = append(errors, common.Error{
				Text: fmt.Sprintf("Node has disk pressure: %s", condition.Message),
			})
		}
	}

	// 检查临时存储（ephemeral-storage）
	if ephemeralStorage, ok := node.Status.Allocatable[v1.ResourceEphemeralStorage]; ok {
		if capacity, ok := node.Status.Capacity[v1.ResourceEphemeralStorage]; ok {
			allocatableBytes := ephemeralStorage.Value()
			capacityBytes := capacity.Value()

			// 如果可分配空间低于容量的 20%，发出警告
			if allocatableBytes > 0 && capacityBytes > 0 {
				usagePercent := float64(capacityBytes-allocatableBytes) / float64(capacityBytes) * 100
				if usagePercent > 80 {
					errors = append(errors, common.Error{
						Text: fmt.Sprintf("Node ephemeral storage usage is high: %.2f%%", usagePercent),
					})
				}
			}
		}
	}

	// 检查磁盘相关的污点
	for _, taint := range node.Spec.Taints {
		if strings.Contains(taint.Key, "disk") || taint.Key == v1.TaintNodeDiskPressure {
			errors = append(errors, common.Error{
				Text: fmt.Sprintf("Node has disk-related taint: %s=%s:%s",
					taint.Key, taint.Value, taint.Effect),
			})
		}
	}

	return errors
}

// GetNodeDiskUsage 获取节点磁盘使用情况（需要 metrics-server）
func (a *DiskAnalyzer) GetNodeDiskUsage(ctx context.Context, client *kubernetes.Clientset, nodeName string) (float64, error) {
	// 这需要 metrics-server API
	// 实际使用中需要调用 metrics.k8s.io API
	// 这里返回一个占位符
	return 0, fmt.Errorf("metrics API not implemented")
}

// DiskInfo 磁盘信息
type DiskInfo struct {
	Device     string  `json:"device"`
	MountPoint string  `json:"mountPoint"`
	Used       int64   `json:"used"`
	Available  int64   `json:"available"`
	UsagePercent float64 `json:"usagePercent"`
}

// ParseDiskUsageFromDF 从 df 命令输出解析磁盘使用情况
func ParseDiskUsageFromDF(output string) []DiskInfo {
	var disks []DiskInfo

	lines := strings.Split(output, "\n")
	for i, line := range lines {
		if i == 0 || line == "" {
			continue // 跳过标题行和空行
		}

		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}

		// df 输出格式: Filesystem 1K-blocks Used Available Use% Mounted on
		device := fields[0]
		mountPoint := fields[5]
		usageStr := strings.TrimSuffix(fields[4], "%")

		used, _ := strconv.ParseInt(fields[2], 10, 64)
		available, _ := strconv.ParseInt(fields[3], 10, 64)
		usagePercent, _ := strconv.ParseFloat(usageStr, 64)

		disks = append(disks, DiskInfo{
			Device:       device,
			MountPoint:   mountPoint,
			Used:         used * 1024, // 转换为字节
			Available:    available * 1024,
			UsagePercent: usagePercent,
		})
	}

	return disks
}
