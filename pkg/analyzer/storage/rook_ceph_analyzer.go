package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/analyzer"
	"github.com/microyahoo/infra-agent/pkg/common"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// RookCephAnalyzer Rook Ceph 存储分析器
type RookCephAnalyzer struct {
	analyzer.BaseAnalyzer
}

// NewRookCephAnalyzer 创建 Rook Ceph 分析器
func NewRookCephAnalyzer() *RookCephAnalyzer {
	return &RookCephAnalyzer{
		BaseAnalyzer: analyzer.BaseAnalyzer{
			Name:    "RookCephAnalyzer",
			Kind:    "RookCeph",
			Enabled: true,
		},
	}
}

// Analyze 执行 Rook Ceph 分析
func (a *RookCephAnalyzer) Analyze(ctx context.Context, client *kubernetes.Clientset, namespace string) ([]common.Result, error) {
	var results []common.Result

	// 检查 Rook Ceph 命名空间（通常是 rook-ceph）
	rookNamespace := "rook-ceph"
	if namespace != "" && namespace != rookNamespace {
		// 如果指定了其他命名空间，跳过
		return results, nil
	}

	// 1. 检查 Ceph 集群 Pod
	cephPods, err := client.CoreV1().Pods(rookNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		// Rook 可能未安装
		return results, nil
	}

	if len(cephPods.Items) == 0 {
		// Rook Ceph 未安装，跳过
		return results, nil
	}

	// 2. 检查关键组件的 Pod 状态
	for _, pod := range cephPods.Items {
		if errs := a.analyzeCephPod(&pod); len(errs) > 0 {
			results = append(results, common.Result{
				Kind:      "RookCeph",
				Name:      pod.Name,
				Namespace: pod.Namespace,
				Error:     errs,
				Details:   fmt.Sprintf("Component: %s", getCephComponent(pod.Name)),
			})
		}
	}

	// 3. 检查 Ceph 相关的 Deployment
	deployments, err := client.AppsV1().Deployments(rookNamespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, deploy := range deployments.Items {
			if errs := a.analyzeCephDeployment(&deploy); len(errs) > 0 {
				results = append(results, common.Result{
					Kind:      "RookCeph",
					Name:      deploy.Name,
					Namespace: deploy.Namespace,
					Error:     errs,
					Details:   "Type: Deployment",
				})
			}
		}
	}

	// 4. 检查 StorageClass
	storageClasses, err := client.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, sc := range storageClasses.Items {
			// 检查 Rook Ceph 的 StorageClass
			if strings.Contains(sc.Provisioner, "rook-ceph") {
				if errs := a.analyzeStorageClass(&sc); len(errs) > 0 {
					results = append(results, common.Result{
						Kind:      "RookCeph",
						Name:      sc.Name,
						Namespace: "",
						Error:     errs,
						Details:   fmt.Sprintf("Type: StorageClass, Provisioner: %s", sc.Provisioner),
					})
				}
			}
		}
	}

	// 5. 检查 Ceph 相关的 PVC 问题
	pvcs, err := client.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, pvc := range pvcs.Items {
			if pvc.Spec.StorageClassName != nil && strings.Contains(*pvc.Spec.StorageClassName, "rook") {
				if errs := a.analyzeCephPVC(&pvc); len(errs) > 0 {
					results = append(results, common.Result{
						Kind:      "RookCeph",
						Name:      pvc.Name,
						Namespace: pvc.Namespace,
						Error:     errs,
						Details:   fmt.Sprintf("Type: PVC, StorageClass: %s", *pvc.Spec.StorageClassName),
					})
				}
			}
		}
	}

	return results, nil
}

// analyzeCephPod 分析 Ceph Pod
func (a *RookCephAnalyzer) analyzeCephPod(pod *v1.Pod) []common.Error {
	var errors []common.Error

	// 检查关键组件
	component := getCephComponent(pod.Name)

	// 检查 Pod 状态
	if pod.Status.Phase == v1.PodFailed {
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("Ceph %s pod is in Failed state", component),
		})
	}

	if pod.Status.Phase == v1.PodPending {
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("Ceph %s pod is stuck in Pending state", component),
		})
	}

	// 检查容器状态
	for _, containerStatus := range pod.Status.ContainerStatuses {
		// 高重启次数
		if containerStatus.RestartCount > 3 {
			errors = append(errors, common.Error{
				Text: fmt.Sprintf("Ceph %s container %s has high restart count: %d",
					component, containerStatus.Name, containerStatus.RestartCount),
			})
		}

		// CrashLoopBackOff
		if containerStatus.State.Waiting != nil {
			if strings.Contains(containerStatus.State.Waiting.Reason, "CrashLoopBackOff") {
				errors = append(errors, common.Error{
					Text: fmt.Sprintf("Ceph %s container %s is in CrashLoopBackOff",
						component, containerStatus.Name),
				})
			}
		}

		// 未就绪
		if !containerStatus.Ready && pod.Status.Phase == v1.PodRunning {
			errors = append(errors, common.Error{
				Text: fmt.Sprintf("Ceph %s container %s is not ready",
					component, containerStatus.Name),
			})
		}
	}

	return errors
}

// analyzeCephDeployment 分析 Ceph Deployment
func (a *RookCephAnalyzer) analyzeCephDeployment(deployment *appsv1.Deployment) []common.Error {
	var errors []common.Error

	replicas := int32(1)
	if deployment.Spec.Replicas != nil {
		replicas = *deployment.Spec.Replicas
	}

	// 检查副本状态
	if deployment.Status.AvailableReplicas < replicas {
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("Ceph deployment has %d/%d available replicas",
				deployment.Status.AvailableReplicas, replicas),
		})
	}

	if deployment.Status.UnavailableReplicas > 0 {
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("Ceph deployment has %d unavailable replicas",
				deployment.Status.UnavailableReplicas),
		})
	}

	return errors
}

// analyzeStorageClass 分析 StorageClass
func (a *RookCephAnalyzer) analyzeStorageClass(sc *storagev1.StorageClass) []common.Error {
	var errors []common.Error

	// 检查回收策略
	if sc.ReclaimPolicy != nil && *sc.ReclaimPolicy == v1.PersistentVolumeReclaimDelete {
		// 这只是一个警告，Delete 策略在生产环境需要谨慎
		// 但不算错误，所以这里不添加
	}

	// 检查卷绑定模式
	if sc.VolumeBindingMode != nil && *sc.VolumeBindingMode == storagev1.VolumeBindingImmediate {
		// Immediate 模式可能导致调度问题
		// 但也是合理配置，这里不添加错误
	}

	return errors
}

// analyzeCephPVC 分析 Ceph PVC
func (a *RookCephAnalyzer) analyzeCephPVC(pvc *v1.PersistentVolumeClaim) []common.Error {
	var errors []common.Error

	// 检查 PVC 状态
	if pvc.Status.Phase == v1.ClaimPending {
		errors = append(errors, common.Error{
			Text: "Ceph-backed PVC is in Pending state, volume provisioning may have failed",
		})
	}

	if pvc.Status.Phase == v1.ClaimLost {
		errors = append(errors, common.Error{
			Text: "Ceph-backed PVC is in Lost state, the underlying Ceph volume may be lost",
		})
	}

	return errors
}

// getCephComponent 从 Pod 名称识别 Ceph 组件
func getCephComponent(podName string) string {
	switch {
	case strings.Contains(podName, "mon"):
		return "Monitor"
	case strings.Contains(podName, "mgr"):
		return "Manager"
	case strings.Contains(podName, "osd"):
		return "OSD"
	case strings.Contains(podName, "mds"):
		return "MDS"
	case strings.Contains(podName, "rgw"):
		return "RGW"
	case strings.Contains(podName, "rbd-mirror"):
		return "RBD-Mirror"
	case strings.Contains(podName, "operator"):
		return "Operator"
	case strings.Contains(podName, "csi"):
		return "CSI"
	default:
		return "Unknown"
	}
}
