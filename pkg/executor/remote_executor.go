package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// RemoteExecutor 远程命令执行器
type RemoteExecutor struct {
	clientset  *kubernetes.Clientset
	restConfig *rest.Config
}

// NewRemoteExecutor 创建远程执行器
func NewRemoteExecutor(clientset *kubernetes.Clientset, restConfig *rest.Config) *RemoteExecutor {
	return &RemoteExecutor{
		clientset:  clientset,
		restConfig: restConfig,
	}
}

// ExecuteCommand 在 Pod 中执行命令
func (e *RemoteExecutor) ExecuteCommand(ctx context.Context, namespace, podName, containerName string, command []string) (string, string, error) {
	req := e.clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   command,
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(e.restConfig, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("failed to create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	return stdout.String(), stderr.String(), err
}

// GetPodLogs 获取 Pod 日志
func (e *RemoteExecutor) GetPodLogs(ctx context.Context, namespace, podName, containerName string, lines int64, previous bool) (string, error) {
	opts := &corev1.PodLogOptions{
		Container: containerName,
		TailLines: &lines,
		Previous:  previous,
	}

	req := e.clientset.CoreV1().Pods(namespace).GetLogs(podName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get logs: %w", err)
	}
	defer stream.Close()

	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, stream)
	if err != nil {
		return "", fmt.Errorf("failed to read logs: %w", err)
	}

	return buf.String(), nil
}

// GetNodeLogs 获取节点日志（通过特权 Pod）
func (e *RemoteExecutor) GetNodeLogs(ctx context.Context, nodeName string, logPath string, lines int) (string, error) {
	// 在节点上执行命令读取日志
	// 这需要一个 DaemonSet 或者特权 Pod
	// TODO: 实现节点日志收集
	return "", fmt.Errorf("node log collection not implemented yet")
}

// DiagnosticCommands 诊断命令集合
type DiagnosticCommands struct {
	// Pod 诊断命令
	PodCommands []DiagnosticCommand
	// 节点诊断命令
	NodeCommands []DiagnosticCommand
}

// DiagnosticCommand 诊断命令
type DiagnosticCommand struct {
	Name        string   // 命令名称
	Description string   // 命令描述
	Command     []string // 命令参数
	Category    string   // 分类：network, disk, process, system
}

// GetCommonDiagnosticCommands 获取常用诊断命令
func GetCommonDiagnosticCommands() *DiagnosticCommands {
	return &DiagnosticCommands{
		PodCommands: []DiagnosticCommand{
			{
				Name:        "check-process",
				Description: "检查进程状态",
				Command:     []string{"sh", "-c", "ps aux"},
				Category:    "process",
			},
			{
				Name:        "check-disk",
				Description: "检查磁盘使用情况",
				Command:     []string{"sh", "-c", "df -h"},
				Category:    "disk",
			},
			{
				Name:        "check-memory",
				Description: "检查内存使用情况",
				Command:     []string{"sh", "-c", "free -h"},
				Category:    "system",
			},
			{
				Name:        "check-network",
				Description: "检查网络连接",
				Command:     []string{"sh", "-c", "netstat -tulpn"},
				Category:    "network",
			},
			{
				Name:        "check-dns",
				Description: "检查 DNS 解析",
				Command:     []string{"sh", "-c", "nslookup kubernetes.default.svc.cluster.local"},
				Category:    "network",
			},
			{
				Name:        "check-env",
				Description: "检查环境变量",
				Command:     []string{"sh", "-c", "env"},
				Category:    "system",
			},
		},
		NodeCommands: []DiagnosticCommand{
			{
				Name:        "check-kubelet",
				Description: "检查 kubelet 日志",
				Command:     []string{"journalctl", "-u", "kubelet", "-n", "100", "--no-pager"},
				Category:    "system",
			},
			{
				Name:        "check-docker",
				Description: "检查容器运行时日志",
				Command:     []string{"journalctl", "-u", "docker", "-n", "100", "--no-pager"},
				Category:    "system",
			},
			{
				Name:        "check-disk-io",
				Description: "检查磁盘 I/O",
				Command:     []string{"iostat", "-x", "1", "3"},
				Category:    "disk",
			},
			{
				Name:        "check-ceph-status",
				Description: "检查 Ceph 集群状态",
				Command:     []string{"ceph", "status"},
				Category:    "storage",
			},
			{
				Name:        "check-ceph-health",
				Description: "检查 Ceph 健康详情",
				Command:     []string{"ceph", "health", "detail"},
				Category:    "storage",
			},
			{
				Name:        "check-ceph-osd",
				Description: "检查 Ceph OSD 状态",
				Command:     []string{"ceph", "osd", "tree"},
				Category:    "storage",
			},
		},
	}
}

// ExecutionResult 执行结果
type ExecutionResult struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	Error       error  `json:"error,omitempty"`
}
