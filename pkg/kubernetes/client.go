package kubernetes

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NewClient 创建 Kubernetes 客户端
func NewClient(kubeconfig string) (*kubernetes.Clientset, error) {
	clientset, _, err := NewClientWithConfig(kubeconfig)
	return clientset, err
}

// NewClientWithConfig 创建 Kubernetes 客户端并返回配置
func NewClientWithConfig(kubeconfig string) (*kubernetes.Clientset, *rest.Config, error) {
	var config *rest.Config
	var err error

	// 如果未指定 kubeconfig，尝试使用集群内配置
	if kubeconfig == "" {
		config, err = rest.InClusterConfig()
		if err != nil {
			// 如果不在集群内，使用默认的 kubeconfig 路径
			kubeconfig = getDefaultKubeconfig()
		}
	}

	// 如果仍然没有配置，使用 kubeconfig 文件
	if config == nil {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to build config: %w", err)
		}
	}

	// 创建客户端
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	return clientset, config, nil
}

// getDefaultKubeconfig 获取默认的 kubeconfig 路径
func getDefaultKubeconfig() string {
	// 优先使用 KUBECONFIG 环境变量
	if kubeconfig := os.Getenv("KUBECONFIG"); kubeconfig != "" {
		return kubeconfig
	}

	// 使用 ~/.kube/config
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(home, ".kube", "config")
}
