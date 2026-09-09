package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/analyzer"
	"github.com/microyahoo/infra-agent/pkg/common"
	"github.com/microyahoo/infra-agent/pkg/executor"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// RemoteStorageAnalyzer 远程存储服务器分析器
// 综合 storage-bot 的 SSH 远程执行能力和 infra-agent 的智能分析
type RemoteStorageAnalyzer struct {
	analyzer.BaseAnalyzer
	storageServers []executor.StorageServerInfo
	sshExecutors   map[string]*executor.StorageCommandExecutor
}

// NewRemoteStorageAnalyzer 创建远程存储分析器
func NewRemoteStorageAnalyzer(servers []executor.StorageServerInfo) *RemoteStorageAnalyzer {
	return &RemoteStorageAnalyzer{
		BaseAnalyzer: analyzer.BaseAnalyzer{
			Name:    "RemoteStorageAnalyzer",
			Kind:    "RemoteStorage",
			Enabled: true,
		},
		storageServers: servers,
		sshExecutors:   make(map[string]*executor.StorageCommandExecutor),
	}
}

// Analyze 执行远程存储服务器分析
func (a *RemoteStorageAnalyzer) Analyze(ctx context.Context, client *kubernetes.Clientset, namespace string) ([]common.Result, error) {
	var results []common.Result

	// 如果没有配置存储服务器，尝试从 ConfigMap 或环境变量读取
	if len(a.storageServers) == 0 {
		servers, err := a.discoverStorageServers(ctx, client)
		if err != nil || len(servers) == 0 {
			// 没有配置远程存储服务器，跳过
			return results, nil
		}
		a.storageServers = servers
	}

	// 对每个存储服务器执行检查
	for _, server := range a.storageServers {
		serverResults := a.analyzeServer(ctx, &server)
		results = append(results, serverResults...)
	}

	return results, nil
}

// analyzeServer 分析单个存储服务器
func (a *RemoteStorageAnalyzer) analyzeServer(ctx context.Context, server *executor.StorageServerInfo) []common.Result {
	var results []common.Result

	// 获取或创建 SSH 执行器
	executor, err := a.getOrCreateExecutor(server)
	if err != nil {
		results = append(results, common.Result{
			Kind:      "RemoteStorage",
			Name:      server.Host,
			Namespace: "",
			Error: []common.Error{
				{Text: fmt.Sprintf("Failed to connect to storage server: %v", err)},
			},
			Details: fmt.Sprintf("Role: %s", server.Role),
		})
		return results
	}

	// 1. 检查 Ceph 状态
	if errs := a.checkCephStatus(ctx, executor, server); len(errs) > 0 {
		results = append(results, common.Result{
			Kind:      "RemoteStorage",
			Name:      server.Host,
			Namespace: "",
			Error:     errs,
			Details:   fmt.Sprintf("Type: CephStatus, Role: %s", server.Role),
		})
	}

	// 2. 检查磁盘使用情况
	if errs := a.checkDiskUsage(ctx, executor, server); len(errs) > 0 {
		results = append(results, common.Result{
			Kind:      "RemoteStorage",
			Name:      server.Host,
			Namespace: "",
			Error:     errs,
			Details:   fmt.Sprintf("Type: DiskUsage, Role: %s", server.Role),
		})
	}

	// 3. 检查 OSD 状态（仅对 OSD 节点）
	if server.Role == "osd" {
		if errs := a.checkOSDStatus(ctx, executor, server); len(errs) > 0 {
			results = append(results, common.Result{
				Kind:      "RemoteStorage",
				Name:      server.Host,
				Namespace: "",
				Error:     errs,
				Details:   fmt.Sprintf("Type: OSDStatus, Role: %s", server.Role),
			})
		}
	}

	// 4. 检查进程状态
	if errs := a.checkProcessStatus(ctx, executor, server); len(errs) > 0 {
		results = append(results, common.Result{
			Kind:      "RemoteStorage",
			Name:      server.Host,
			Namespace: "",
			Error:     errs,
			Details:   fmt.Sprintf("Type: ProcessStatus, Role: %s", server.Role),
		})
	}

	return results
}

// checkCephStatus 检查 Ceph 状态
func (a *RemoteStorageAnalyzer) checkCephStatus(ctx context.Context, exec *executor.StorageCommandExecutor, server *executor.StorageServerInfo) []common.Error {
	var errors []common.Error

	status, err := exec.CheckCephStatus(ctx)
	if err != nil {
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("Failed to get Ceph status: %v", err),
		})
		return errors
	}

	// 检查健康状态
	if status.Health == "WARN" {
		errors = append(errors, common.Error{
			Text: "Ceph cluster health is WARN",
		})
	} else if status.Health == "ERR" {
		errors = append(errors, common.Error{
			Text: "Ceph cluster health is ERR (critical)",
		})
	}

	return errors
}

// checkDiskUsage 检查磁盘使用情况
func (a *RemoteStorageAnalyzer) checkDiskUsage(ctx context.Context, exec *executor.StorageCommandExecutor, server *executor.StorageServerInfo) []common.Error {
	var errors []common.Error

	disks, err := exec.CheckDiskUsage(ctx)
	if err != nil {
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("Failed to get disk usage: %v", err),
		})
		return errors
	}

	// 检查磁盘使用率
	for _, disk := range disks {
		if disk.UsePercent > 90 {
			errors = append(errors, common.Error{
				Text: fmt.Sprintf("Disk %s usage is critical: %.2f%% (mounted at %s)",
					disk.Filesystem, disk.UsePercent, disk.MountPoint),
			})
		} else if disk.UsePercent > 80 {
			errors = append(errors, common.Error{
				Text: fmt.Sprintf("Disk %s usage is high: %.2f%% (mounted at %s)",
					disk.Filesystem, disk.UsePercent, disk.MountPoint),
			})
		}
	}

	return errors
}

// checkOSDStatus 检查 OSD 状态
func (a *RemoteStorageAnalyzer) checkOSDStatus(ctx context.Context, exec *executor.StorageCommandExecutor, server *executor.StorageServerInfo) []common.Error {
	var errors []common.Error

	status, err := exec.CheckOSDStatus(ctx)
	if err != nil {
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("Failed to get OSD status: %v", err),
		})
		return errors
	}

	// 检查 OSD 状态
	if status.UpOSDs < status.TotalOSDs {
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("Some OSDs are down: %d/%d up",
				status.UpOSDs, status.TotalOSDs),
		})
	}

	if status.InOSDs < status.TotalOSDs {
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("Some OSDs are out: %d/%d in",
				status.InOSDs, status.TotalOSDs),
		})
	}

	return errors
}

// checkProcessStatus 检查进程状态
func (a *RemoteStorageAnalyzer) checkProcessStatus(ctx context.Context, exec *executor.StorageCommandExecutor, server *executor.StorageServerInfo) []common.Error {
	var errors []common.Error

	// 根据角色检查不同的进程
	var processName string
	switch server.Role {
	case "mon":
		processName = "ceph-mon"
	case "osd":
		processName = "ceph-osd"
	case "mgr":
		processName = "ceph-mgr"
	case "mds":
		processName = "ceph-mds"
	default:
		return errors
	}

	processes, err := exec.CheckProcessStatus(ctx, processName)
	if err != nil {
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("Failed to check process status: %v", err),
		})
		return errors
	}

	if len(processes) == 0 {
		errors = append(errors, common.Error{
			Text: fmt.Sprintf("Process %s is not running", processName),
		})
	}

	return errors
}

// getOrCreateExecutor 获取或创建 SSH 执行器
func (a *RemoteStorageAnalyzer) getOrCreateExecutor(server *executor.StorageServerInfo) (*executor.StorageCommandExecutor, error) {
	key := fmt.Sprintf("%s:%d", server.Host, server.Port)

	if exec, ok := a.sshExecutors[key]; ok {
		return exec, nil
	}

	exec, err := executor.NewStorageCommandExecutor(server)
	if err != nil {
		return nil, err
	}

	a.sshExecutors[key] = exec
	return exec, nil
}

// discoverStorageServers 从 K8s 配置中发现存储服务器
func (a *RemoteStorageAnalyzer) discoverStorageServers(ctx context.Context, client *kubernetes.Clientset) ([]executor.StorageServerInfo, error) {
	var servers []executor.StorageServerInfo

	// 尝试从 ConfigMap 读取存储服务器配置
	// 配置格式: storage-servers ConfigMap in kube-system namespace
	cm, err := client.CoreV1().ConfigMaps("kube-system").Get(ctx, "storage-servers", metav1.GetOptions{})
	if err != nil {
		return servers, nil // 没有配置，返回空
	}

	// 解析配置
	// 格式: host:port:user:password:role (每行一个)
	if data, ok := cm.Data["servers"]; ok {
		lines := strings.Split(data, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			parts := strings.Split(line, ":")
			if len(parts) < 5 {
				continue
			}

			port := 22
			fmt.Sscanf(parts[1], "%d", &port)

			servers = append(servers, executor.StorageServerInfo{
				Host:     parts[0],
				Port:     port,
				User:     parts[2],
				Password: parts[3],
				Role:     parts[4],
			})
		}
	}

	return servers, nil
}

// CloseAll 关闭所有 SSH 连接
func (a *RemoteStorageAnalyzer) CloseAll() {
	for _, exec := range a.sshExecutors {
		exec.Close()
	}
}
