package k8s

import (
	"context"
	"fmt"

	"github.com/microyahoo/infra-agent/pkg/analyzer"
	"github.com/microyahoo/infra-agent/pkg/common"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ServiceAnalyzer Service 分析器
type ServiceAnalyzer struct {
	analyzer.BaseAnalyzer
}

// NewServiceAnalyzer 创建 Service 分析器
func NewServiceAnalyzer() *ServiceAnalyzer {
	return &ServiceAnalyzer{
		BaseAnalyzer: analyzer.BaseAnalyzer{
			Name:    "ServiceAnalyzer",
			Kind:    "Service",
			Enabled: true,
		},
	}
}

// Analyze 执行 Service 分析
func (a *ServiceAnalyzer) Analyze(ctx context.Context, client *kubernetes.Clientset, namespace string) ([]common.Result, error) {
	var results []common.Result

	services, err := client.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list services: %w", err)
	}

	for _, service := range services.Items {
		if errs := a.analyzeService(ctx, client, &service); len(errs) > 0 {
			results = append(results, common.Result{
				Kind:      "Service",
				Name:      service.Name,
				Namespace: service.Namespace,
				Error:     errs,
			})
		}
	}

	return results, nil
}

// analyzeService 分析 Service
func (a *ServiceAnalyzer) analyzeService(ctx context.Context, client *kubernetes.Clientset, service *v1.Service) []common.Error {
	var errors []common.Error

	// 跳过 Kubernetes 系统服务
	if service.Name == "kubernetes" {
		return errors
	}

	// 检查选择器
	if len(service.Spec.Selector) == 0 && service.Spec.Type != v1.ServiceTypeExternalName {
		errors = append(errors, common.Error{
			Text: "Service has no selector defined",
		})
		return errors
	}

	// 检查是否有匹配的 Pod
	if service.Spec.Type != v1.ServiceTypeExternalName {
		pods, err := client.CoreV1().Pods(service.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: metav1.FormatLabelSelector(&metav1.LabelSelector{
				MatchLabels: service.Spec.Selector,
			}),
		})

		if err == nil && len(pods.Items) == 0 {
			errors = append(errors, common.Error{
				Text: "Service has no matching pods",
			})
		}
	}

	// 检查 LoadBalancer 类型的服务
	if service.Spec.Type == v1.ServiceTypeLoadBalancer {
		if len(service.Status.LoadBalancer.Ingress) == 0 {
			errors = append(errors, common.Error{
				Text: "LoadBalancer service has no external IP assigned",
			})
		}
	}

	return errors
}
