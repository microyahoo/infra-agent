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

// StatefulSetAnalyzer StatefulSet 分析器
type StatefulSetAnalyzer struct {
	analyzer.BaseAnalyzer
}

// NewStatefulSetAnalyzer 创建 StatefulSet 分析器
func NewStatefulSetAnalyzer() *StatefulSetAnalyzer {
	return &StatefulSetAnalyzer{
		BaseAnalyzer: analyzer.BaseAnalyzer{
			Name:    "StatefulSetAnalyzer",
			Kind:    "StatefulSet",
			Enabled: true,
		},
	}
}

// Analyze 执行 StatefulSet 分析
func (a *StatefulSetAnalyzer) Analyze(ctx context.Context, client *kubernetes.Clientset, namespace string) ([]common.Result, error) {
	var results []common.Result

	statefulSets, err := client.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list statefulsets: %w", err)
	}

	for _, sts := range statefulSets.Items {
		if errs := a.analyzeStatefulSet(&sts); len(errs) > 0 {
			results = append(results, common.Result{
				Kind:      "StatefulSet",
				Name:      sts.Name,
				Namespace: sts.Namespace,
				Error:     errs,
			})
		}
	}

	return results, nil
}

// analyzeStatefulSet 分析 StatefulSet
func (a *StatefulSetAnalyzer) analyzeStatefulSet(sts *appsv1.StatefulSet) []common.Error {
	var errors []common.Error

	replicas := int32(1)
	if sts.Spec.Replicas != nil {
		replicas = *sts.Spec.Replicas
	}

	// 检查副本状态
	if sts.Status.ReadyReplicas < replicas {
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("StatefulSet has %d/%d ready replicas",
				sts.Status.ReadyReplicas, replicas),
		})
	}

	// 检查更新状态
	if sts.Status.CurrentRevision != sts.Status.UpdateRevision {
		errors = append(errors, common.Error{
			Text: "StatefulSet is being updated",
		})
	}

	// 检查是否有 PVC 模板但没有存储类
	for _, vct := range sts.Spec.VolumeClaimTemplates {
		if vct.Spec.StorageClassName == nil || *vct.Spec.StorageClassName == "" {
			errors = append(errors, common.Error{
				Text: fmt.Sprintf("VolumeClaimTemplate %s does not specify a StorageClass",
					vct.Name),
			})
		}
	}

	return errors
}
