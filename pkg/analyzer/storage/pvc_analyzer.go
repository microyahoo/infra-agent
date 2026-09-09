package storage

import (
	"context"
	"fmt"

	"github.com/microyahoo/infra-agent/pkg/analyzer"
	"github.com/microyahoo/infra-agent/pkg/common"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PVCAnalyzer PVC 分析器
type PVCAnalyzer struct {
	analyzer.BaseAnalyzer
}

// NewPVCAnalyzer 创建 PVC 分析器
func NewPVCAnalyzer() *PVCAnalyzer {
	return &PVCAnalyzer{
		BaseAnalyzer: analyzer.BaseAnalyzer{
			Name:    "PVCAnalyzer",
			Kind:    "PersistentVolumeClaim",
			Enabled: true,
		},
	}
}

// Analyze 执行 PVC 分析
func (a *PVCAnalyzer) Analyze(ctx context.Context, client *kubernetes.Clientset, namespace string) ([]common.Result, error) {
	var results []common.Result

	// 获取 PVC 列表
	pvcs, err := client.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list PVCs: %w", err)
	}

	for _, pvc := range pvcs.Items {
		if errs := a.analyzePVC(&pvc); len(errs) > 0 {
			results = append(results, common.Result{
				Kind:      "PersistentVolumeClaim",
				Name:      pvc.Name,
				Namespace: pvc.Namespace,
				Error:     errs,
			})
		}
	}

	return results, nil
}

// analyzePVC 分析 PVC
func (a *PVCAnalyzer) analyzePVC(pvc *v1.PersistentVolumeClaim) []common.Error {
	var errors []common.Error

	// 检查 PVC 状态
	switch pvc.Status.Phase {
	case v1.ClaimPending:
		errors = append(errors, common.Error{
			Text: "PVC is in Pending state, waiting for volume to be provisioned",
		})

	case v1.ClaimLost:
		errors = append(errors, common.Error{
			Text: "PVC is in Lost state, the bound PV has been deleted",
		})
	}

	// 检查容量
	if pvc.Status.Capacity != nil && pvc.Spec.Resources.Requests != nil {
		requestedStorage := pvc.Spec.Resources.Requests.Storage()
		actualStorage := pvc.Status.Capacity.Storage()

		if actualStorage.Cmp(*requestedStorage) < 0 {
			errors = append(errors, common.Error{
				Text: fmt.Sprintf("PVC allocated storage (%s) is less than requested (%s)",
					actualStorage.String(), requestedStorage.String()),
			})
		}
	}

	// 检查 StorageClass
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName == "" {
		errors = append(errors, common.Error{
			Text: "PVC does not specify a StorageClass",
		})
	}

	return errors
}
