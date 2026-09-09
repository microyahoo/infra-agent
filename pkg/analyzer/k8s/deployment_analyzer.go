package k8s

import (
	"context"
	"fmt"

	"github.com/microyahoo/infra-agent/pkg/analyzer"
	"github.com/microyahoo/infra-agent/pkg/common"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// DeploymentAnalyzer Deployment 分析器
type DeploymentAnalyzer struct {
	analyzer.BaseAnalyzer
}

// NewDeploymentAnalyzer 创建 Deployment 分析器
func NewDeploymentAnalyzer() *DeploymentAnalyzer {
	return &DeploymentAnalyzer{
		BaseAnalyzer: analyzer.BaseAnalyzer{
			Name:    "DeploymentAnalyzer",
			Kind:    "Deployment",
			Enabled: true,
		},
	}
}

// Analyze 执行 Deployment 分析
func (a *DeploymentAnalyzer) Analyze(ctx context.Context, client *kubernetes.Clientset, namespace string) ([]common.Result, error) {
	var results []common.Result

	deployments, err := client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list deployments: %w", err)
	}

	for _, deployment := range deployments.Items {
		if errs := a.analyzeDeployment(&deployment); len(errs) > 0 {
			results = append(results, common.Result{
				Kind:      "Deployment",
				Name:      deployment.Name,
				Namespace: deployment.Namespace,
				Error:     errs,
			})
		}
	}

	return results, nil
}

// analyzeDeployment 分析 Deployment
func (a *DeploymentAnalyzer) analyzeDeployment(deployment *appsv1.Deployment) []common.Error {
	var errors []common.Error

	// 检查副本状态
	replicas := int32(1)
	if deployment.Spec.Replicas != nil {
		replicas = *deployment.Spec.Replicas
	}

	if deployment.Status.AvailableReplicas < replicas {
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("Deployment has %d/%d available replicas",
				deployment.Status.AvailableReplicas, replicas),
		})
	}

	if deployment.Status.UnavailableReplicas > 0 {
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("Deployment has %d unavailable replicas",
				deployment.Status.UnavailableReplicas),
		})
	}

	// 检查更新状态
	if deployment.Status.ObservedGeneration < deployment.Generation {
		errors = append(errors, common.Error{
			Text: "Deployment update is in progress",
		})
	}

	// 检查条件
	for _, condition := range deployment.Status.Conditions {
		if condition.Status == "False" {
			errors = append(errors, common.Error{
				Text: fmt.Sprintf("Deployment condition %s is False: %s",
					condition.Type, condition.Message),
			})
		}
	}

	return errors
}
