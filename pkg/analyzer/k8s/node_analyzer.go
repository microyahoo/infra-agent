package k8s

import (
	"context"
	"fmt"

	"github.com/microyahoo/infra-agent/pkg/analyzer"
	"github.com/microyahoo/infra-agent/pkg/common"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// NodeAnalyzer Node 分析器
type NodeAnalyzer struct {
	analyzer.BaseAnalyzer
}

// NewNodeAnalyzer 创建 Node 分析器
func NewNodeAnalyzer() *NodeAnalyzer {
	return &NodeAnalyzer{
		BaseAnalyzer: analyzer.BaseAnalyzer{
			Name:    "NodeAnalyzer",
			Kind:    "Node",
			Enabled: true,
		},
	}
}

// Analyze 执行 Node 分析
func (a *NodeAnalyzer) Analyze(ctx context.Context, client *kubernetes.Clientset, namespace string) ([]common.Result, error) {
	var results []common.Result

	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	for _, node := range nodes.Items {
		if errs := a.analyzeNode(ctx, client, &node); len(errs) > 0 {
			results = append(results, common.Result{
				Kind:      "Node",
				Name:      node.Name,
				Namespace: "",
				Error:     errs,
			})
		}
	}

	return results, nil
}

// analyzeNode 分析 Node
func (a *NodeAnalyzer) analyzeNode(ctx context.Context, client *kubernetes.Clientset, node *v1.Node) []common.Error {
	var errors []common.Error

	// 检查节点条件
	for _, condition := range node.Status.Conditions {
		switch condition.Type {
		case v1.NodeReady:
			if condition.Status != v1.ConditionTrue {
				errors = append(errors, common.Error{
					Text: fmt.Sprintf("Node is not ready: %s", condition.Message),
				})
			}
		case v1.NodeMemoryPressure:
			if condition.Status == v1.ConditionTrue {
				errors = append(errors, common.Error{
					Text: fmt.Sprintf("Node has memory pressure: %s", condition.Message),
				})
			}
		case v1.NodeDiskPressure:
			if condition.Status == v1.ConditionTrue {
				errors = append(errors, common.Error{
					Text: fmt.Sprintf("Node has disk pressure: %s", condition.Message),
				})
			}
		case v1.NodePIDPressure:
			if condition.Status == v1.ConditionTrue {
				errors = append(errors, common.Error{
					Text: fmt.Sprintf("Node has PID pressure: %s", condition.Message),
				})
			}
		case v1.NodeNetworkUnavailable:
			if condition.Status == v1.ConditionTrue {
				errors = append(errors, common.Error{
					Text: fmt.Sprintf("Node network is unavailable: %s", condition.Message),
				})
			}
		}
	}

	// 检查资源使用情况
	errors = append(errors, a.checkNodeResources(ctx, client, node)...)

	// 检查污点
	if len(node.Spec.Taints) > 0 {
		for _, taint := range node.Spec.Taints {
			if taint.Effect == v1.TaintEffectNoSchedule || taint.Effect == v1.TaintEffectNoExecute {
				errors = append(errors, common.Error{
					Text: fmt.Sprintf("Node has taint: %s=%s:%s", taint.Key, taint.Value, taint.Effect),
				})
			}
		}
	}

	return errors
}

// checkNodeResources 检查节点资源使用情况
func (a *NodeAnalyzer) checkNodeResources(ctx context.Context, client *kubernetes.Clientset, node *v1.Node) []common.Error {
	var errors []common.Error

	// 获取节点上的所有 Pod
	fieldSelector := fmt.Sprintf("spec.nodeName=%s", node.Name)
	pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: fieldSelector,
	})

	if err != nil {
		return errors
	}

	// 计算资源使用
	var cpuRequests, cpuLimits, memoryRequests, memoryLimits resource.Quantity

	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			if cpu := container.Resources.Requests.Cpu(); cpu != nil {
				cpuRequests.Add(*cpu)
			}
			if cpu := container.Resources.Limits.Cpu(); cpu != nil {
				cpuLimits.Add(*cpu)
			}
			if mem := container.Resources.Requests.Memory(); mem != nil {
				memoryRequests.Add(*mem)
			}
			if mem := container.Resources.Limits.Memory(); mem != nil {
				memoryLimits.Add(*mem)
			}
		}
	}

	// 获取节点容量
	cpuCapacity := node.Status.Allocatable.Cpu()
	memoryCapacity := node.Status.Allocatable.Memory()

	// 检查 CPU 使用率
	if cpuCapacity != nil && !cpuCapacity.IsZero() {
		cpuUsagePercent := float64(cpuRequests.MilliValue()) / float64(cpuCapacity.MilliValue()) * 100
		if cpuUsagePercent > 80 {
			errors = append(errors, common.Error{
				Text: fmt.Sprintf("Node CPU usage is high: %.2f%%", cpuUsagePercent),
			})
		}
	}

	// 检查内存使用率
	if memoryCapacity != nil && !memoryCapacity.IsZero() {
		memoryUsagePercent := float64(memoryRequests.Value()) / float64(memoryCapacity.Value()) * 100
		if memoryUsagePercent > 85 {
			errors = append(errors, common.Error{
				Text: fmt.Sprintf("Node memory usage is high: %.2f%%", memoryUsagePercent),
			})
		}
	}

	return errors
}
