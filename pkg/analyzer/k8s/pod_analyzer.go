package k8s

import (
	"context"
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/analyzer"
	"github.com/microyahoo/infra-agent/pkg/common"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PodAnalyzer Pod 分析器
type PodAnalyzer struct {
	analyzer.BaseAnalyzer
}

// NewPodAnalyzer 创建 Pod 分析器
func NewPodAnalyzer() *PodAnalyzer {
	return &PodAnalyzer{
		BaseAnalyzer: analyzer.BaseAnalyzer{
			Name:    "PodAnalyzer",
			Kind:    "Pod",
			Enabled: true,
		},
	}
}

// Analyze 执行 Pod 分析
func (a *PodAnalyzer) Analyze(ctx context.Context, client *kubernetes.Clientset, namespace string) ([]common.Result, error) {
	var results []common.Result

	// 获取 Pod 列表
	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	for _, pod := range pods.Items {
		// 分析 Pod 状态
		if errs := a.analyzePodStatus(&pod); len(errs) > 0 {
			results = append(results, common.Result{
				Kind:      "Pod",
				Name:      pod.Name,
				Namespace: pod.Namespace,
				Error:     errs,
			})
		}
	}

	return results, nil
}

// analyzePodStatus 分析 Pod 状态
func (a *PodAnalyzer) analyzePodStatus(pod *v1.Pod) []common.Error {
	var errors []common.Error

	// 检查 Pod 阶段
	switch pod.Status.Phase {
	case v1.PodPending:
		// 检查为什么 Pending
		for _, condition := range pod.Status.Conditions {
			if condition.Status == v1.ConditionFalse {
				errors = append(errors, common.Error{
					Text: fmt.Sprintf("Pod is in Pending state: %s - %s", condition.Reason, condition.Message),
				})
			}
		}

		// 检查容器状态
		for _, containerStatus := range pod.Status.ContainerStatuses {
			if containerStatus.State.Waiting != nil {
				waiting := containerStatus.State.Waiting
				errors = append(errors, common.Error{
					Text: fmt.Sprintf("Container %s is waiting: %s - %s",
						containerStatus.Name, waiting.Reason, waiting.Message),
				})
			}
		}

	case v1.PodFailed:
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("Pod is in Failed state: %s", pod.Status.Message),
		})

	case v1.PodUnknown:
		errors = append(errors, common.Error{
			Text: "Pod is in Unknown state",
		})
	}

	// 检查容器重启
	for _, containerStatus := range pod.Status.ContainerStatuses {
		if containerStatus.RestartCount > 5 {
			errors = append(errors, common.Error{
				Text: fmt.Sprintf("Container %s has high restart count: %d",
					containerStatus.Name, containerStatus.RestartCount),
			})
		}

		// 检查容器是否 CrashLoopBackOff
		if containerStatus.State.Waiting != nil {
			if strings.Contains(containerStatus.State.Waiting.Reason, "CrashLoopBackOff") {
				errors = append(errors, common.Error{
					Text: fmt.Sprintf("Container %s is in CrashLoopBackOff: %s",
						containerStatus.Name, containerStatus.State.Waiting.Message),
				})
			}
		}

		// 检查容器是否 OOMKilled
		if containerStatus.LastTerminationState.Terminated != nil {
			if containerStatus.LastTerminationState.Terminated.Reason == "OOMKilled" {
				errors = append(errors, common.Error{
					Text: fmt.Sprintf("Container %s was OOMKilled", containerStatus.Name),
				})
			}
		}
	}

	// 检查资源请求和限制
	for _, container := range pod.Spec.Containers {
		if container.Resources.Requests == nil || container.Resources.Limits == nil {
			errors = append(errors, common.Error{
				Text: fmt.Sprintf("Container %s does not have resource requests or limits set", container.Name),
			})
		}
	}

	return errors
}
