package storage

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/microyahoo/infra-agent/pkg/common"
	"github.com/microyahoo/infra-agent/pkg/executor/ssh"
)

// StorageServerInfo 存储服务器信息
type StorageServerInfo struct {
	Host     string
	Port     int
	User     string
	Password string
	Role     string // osd, mon, mgr, mds, rgw
}

// StorageCommandExecutor 存储服务器命令执行器
type StorageCommandExecutor struct {
	sshExecutor *ssh.SSHExecutor
	serverInfo  *StorageServerInfo
}

// NewStorageCommandExecutor 创建存储命令执行器
func NewStorageCommandExecutor(serverInfo *StorageServerInfo) (*StorageCommandExecutor, error) {
	sshConfig := &ssh.SSHConfig{
		Host:     serverInfo.Host,
		Port:     serverInfo.Port,
		User:     serverInfo.User,
		Password: serverInfo.Password,
	}

	sshExec, err := ssh.NewSSHExecutor(sshConfig)
	if err != nil {
		return nil, err
	}

	return &StorageCommandExecutor{
		sshExecutor: sshExec,
		serverInfo:  serverInfo,
	}, nil
}

// CheckCephStatus 检查 Ceph 状态
func (e *StorageCommandExecutor) CheckCephStatus(ctx context.Context) (*CephStatus, error) {
	stdout, _, err := e.sshExecutor.Execute(ctx, "ceph status -f json")
	if err != nil {
		return nil, fmt.Errorf("failed to get ceph status: %w", err)
	}

	// TODO: 解析 JSON 输出
	status := &CephStatus{
		Health:     parseCephHealth(stdout),
		RawOutput:  stdout,
		Timestamp:  "",
	}

	return status, nil
}

// CheckCephHealth 检查 Ceph 健康详情
func (e *StorageCommandExecutor) CheckCephHealth(ctx context.Context) ([]string, error) {
	stdout, _, err := e.sshExecutor.Execute(ctx, "ceph health detail")
	if err != nil {
		return nil, err
	}

	lines := strings.Split(stdout, "\n")
	return lines, nil
}

// CheckOSDStatus 检查 OSD 状态
func (e *StorageCommandExecutor) CheckOSDStatus(ctx context.Context) (*OSDStatus, error) {
	stdout, _, err := e.sshExecutor.Execute(ctx, "ceph osd tree")
	if err != nil {
		return nil, err
	}

	status := &OSDStatus{
		Tree:      stdout,
		TotalOSDs: countOSDs(stdout),
		UpOSDs:    countUpOSDs(stdout),
		InOSDs:    countInOSDs(stdout),
	}

	return status, nil
}

// CheckDiskUsage 检查磁盘使用情况
func (e *StorageCommandExecutor) CheckDiskUsage(ctx context.Context) ([]DiskUsageInfo, error) {
	stdout, _, err := e.sshExecutor.Execute(ctx, "df -h")
	if err != nil {
		return nil, err
	}

	return parseDiskUsage(stdout), nil
}

// CheckDiskIO 检查磁盘 I/O
func (e *StorageCommandExecutor) CheckDiskIO(ctx context.Context) (string, error) {
	stdout, _, err := e.sshExecutor.Execute(ctx, "iostat -x 1 3")
	if err != nil {
		return "", err
	}

	return stdout, nil
}

// CheckDiskSMART 检查磁盘 SMART 状态
func (e *StorageCommandExecutor) CheckDiskSMART(ctx context.Context, device string) (*SMARTStatus, error) {
	cmd := fmt.Sprintf("smartctl -a %s", device)
	stdout, _, err := e.sshExecutor.Execute(ctx, cmd)
	if err != nil {
		return nil, err
	}

	status := &SMARTStatus{
		Device:    device,
		Health:    parseSMARTHealth(stdout),
		RawOutput: stdout,
	}

	return status, nil
}

// GetCephLogs 获取 Ceph 日志
func (e *StorageCommandExecutor) GetCephLogs(ctx context.Context, service string, lines int) (string, error) {
	var logPath string

	switch service {
	case "mon":
		logPath = "/var/log/ceph/ceph-mon.*.log"
	case "osd":
		logPath = "/var/log/ceph/ceph-osd.*.log"
	case "mgr":
		logPath = "/var/log/ceph/ceph-mgr.*.log"
	case "mds":
		logPath = "/var/log/ceph/ceph-mds.*.log"
	default:
		logPath = "/var/log/ceph/*.log"
	}

	cmd := fmt.Sprintf("tail -n %d %s 2>/dev/null || echo 'No logs found'", lines, logPath)
	stdout, _, err := e.sshExecutor.Execute(ctx, cmd)
	if err != nil {
		return "", err
	}

	return stdout, nil
}

// SearchCephLogs 在 Ceph 日志中搜索
func (e *StorageCommandExecutor) SearchCephLogs(ctx context.Context, service string, pattern string, lines int) (string, error) {
	var logPath string

	switch service {
	case "mon":
		logPath = "/var/log/ceph/ceph-mon.*.log"
	case "osd":
		logPath = "/var/log/ceph/ceph-osd.*.log"
	default:
		logPath = "/var/log/ceph/*.log"
	}

	cmd := fmt.Sprintf("grep -i '%s' %s | tail -n %d", pattern, logPath, lines)
	stdout, _, err := e.sshExecutor.Execute(ctx, cmd)
	if err != nil {
		return "", err
	}

	return stdout, nil
}

// GetSystemLogs 获取系统日志
func (e *StorageCommandExecutor) GetSystemLogs(ctx context.Context, service string, lines int) (string, error) {
	cmd := fmt.Sprintf("journalctl -u %s -n %d --no-pager", service, lines)
	stdout, _, err := e.sshExecutor.Execute(ctx, cmd)
	if err != nil {
		return "", err
	}

	return stdout, nil
}

// CheckProcessStatus 检查进程状态
func (e *StorageCommandExecutor) CheckProcessStatus(ctx context.Context, processName string) ([]ProcessInfo, error) {
	cmd := fmt.Sprintf("ps aux | grep '%s' | grep -v grep", processName)
	stdout, _, err := e.sshExecutor.Execute(ctx, cmd)
	if err != nil {
		return nil, err
	}

	return parseProcessInfo(stdout), nil
}

// CheckNetworkConnections 检查网络连接
func (e *StorageCommandExecutor) CheckNetworkConnections(ctx context.Context, port int) (string, error) {
	cmd := fmt.Sprintf("netstat -tulpn | grep ':%d'", port)
	stdout, _, err := e.sshExecutor.Execute(ctx, cmd)
	if err != nil {
		return "", err
	}

	return stdout, nil
}

// Close 关闭连接
func (e *StorageCommandExecutor) Close() error {
	return e.sshExecutor.Close()
}

// 数据结构

type CephStatus struct {
	Health    string
	RawOutput string
	Timestamp string
}

type OSDStatus struct {
	Tree      string
	TotalOSDs int
	UpOSDs    int
	InOSDs    int
}

type DiskUsageInfo struct {
	Filesystem string
	Size       string
	Used       string
	Avail      string
	UsePercent float64
	MountPoint string
}

type SMARTStatus struct {
	Device    string
	Health    string
	RawOutput string
}

type ProcessInfo struct {
	User    string
	PID     int
	CPU     float64
	Memory  float64
	Command string
}

// 辅助函数

func parseCephHealth(output string) string {
	// 简单实现
	if strings.Contains(output, "HEALTH_OK") {
		return "OK"
	}
	if strings.Contains(output, "HEALTH_WARN") {
		return "WARN"
	}
	if strings.Contains(output, "HEALTH_ERR") {
		return "ERR"
	}
	return "UNKNOWN"
}

func countOSDs(output string) int {
	count := 0
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "osd.") {
			count++
		}
	}
	return count
}

func countUpOSDs(output string) int {
	count := 0
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "osd.") && strings.Contains(line, "up") {
			count++
		}
	}
	return count
}

func countInOSDs(output string) int {
	count := 0
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "osd.") && strings.Contains(line, "in") {
			count++
		}
	}
	return count
}

func parseDiskUsage(output string) []DiskUsageInfo {
	var disks []DiskUsageInfo
	lines := strings.Split(output, "\n")

	for i, line := range lines {
		if i == 0 {
			continue // 跳过标题行
		}

		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}

		usePercentStr := strings.TrimSuffix(fields[4], "%")
		usePercent, _ := strconv.ParseFloat(usePercentStr, 64)

		disks = append(disks, DiskUsageInfo{
			Filesystem: fields[0],
			Size:       fields[1],
			Used:       fields[2],
			Avail:      fields[3],
			UsePercent: usePercent,
			MountPoint: fields[5],
		})
	}

	return disks
}

func parseSMARTHealth(output string) string {
	if strings.Contains(output, "PASSED") {
		return "PASSED"
	}
	if strings.Contains(output, "FAILED") {
		return "FAILED"
	}
	return "UNKNOWN"
}

func parseProcessInfo(output string) []ProcessInfo {
	var processes []ProcessInfo
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 11 {
			continue
		}

		pid, _ := strconv.Atoi(fields[1])
		cpu, _ := strconv.ParseFloat(fields[2], 64)
		mem, _ := strconv.ParseFloat(fields[3], 64)

		processes = append(processes, ProcessInfo{
			User:    fields[0],
			PID:     pid,
			CPU:     cpu,
			Memory:  mem,
			Command: strings.Join(fields[10:], " "),
		})
	}

	return processes
}
