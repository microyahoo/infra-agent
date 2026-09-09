package ssh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHConfig SSH 连接配置
type SSHConfig struct {
	Host       string
	Port       int
	User       string
	Password   string
	PrivateKey string
	Timeout    time.Duration
}

// SSHExecutor SSH 远程执行器
type SSHExecutor struct {
	config *SSHConfig
	client *ssh.Client
}

// NewSSHExecutor 创建 SSH 执行器
func NewSSHExecutor(config *SSHConfig) (*SSHExecutor, error) {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	return &SSHExecutor{
		config: config,
	}, nil
}

// Connect 连接到远程服务器
func (e *SSHExecutor) Connect() error {
	var authMethod ssh.AuthMethod

	if e.config.PrivateKey != "" {
		// 使用私钥认证
		signer, err := ssh.ParsePrivateKey([]byte(e.config.PrivateKey))
		if err != nil {
			return fmt.Errorf("failed to parse private key: %w", err)
		}
		authMethod = ssh.PublicKeys(signer)
	} else {
		// 使用密码认证
		authMethod = ssh.Password(e.config.Password)
	}

	config := &ssh.ClientConfig{
		User: e.config.User,
		Auth: []ssh.AuthMethod{authMethod},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 生产环境应该验证 host key
		Timeout:         e.config.Timeout,
	}

	addr := fmt.Sprintf("%s:%d", e.config.Host, e.config.Port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}

	e.client = client
	return nil
}

// Execute 执行远程命令
func (e *SSHExecutor) Execute(ctx context.Context, command string) (string, string, error) {
	if e.client == nil {
		if err := e.Connect(); err != nil {
			return "", "", err
		}
	}

	session, err := e.client.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// 创建一个带超时的 context
	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()

	select {
	case <-ctx.Done():
		session.Signal(ssh.SIGKILL)
		return stdout.String(), stderr.String(), ctx.Err()
	case err := <-done:
		return stdout.String(), stderr.String(), err
	}
}

// ExecuteScript 执行远程脚本
func (e *SSHExecutor) ExecuteScript(ctx context.Context, script string) (string, string, error) {
	if e.client == nil {
		if err := e.Connect(); err != nil {
			return "", "", err
		}
	}

	session, err := e.client.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// 通过 stdin 传递脚本
	stdin, err := session.StdinPipe()
	if err != nil {
		return "", "", fmt.Errorf("failed to get stdin: %w", err)
	}

	// 启动 shell
	if err := session.Start("/bin/bash"); err != nil {
		return "", "", fmt.Errorf("failed to start shell: %w", err)
	}

	// 写入脚本
	if _, err := io.WriteString(stdin, script); err != nil {
		return "", "", fmt.Errorf("failed to write script: %w", err)
	}
	stdin.Close()

	// 等待执行完成
	done := make(chan error, 1)
	go func() {
		done <- session.Wait()
	}()

	select {
	case <-ctx.Done():
		session.Signal(ssh.SIGKILL)
		return stdout.String(), stderr.String(), ctx.Err()
	case err := <-done:
		return stdout.String(), stderr.String(), err
	}
}

// UploadFile 上传文件到远程服务器
func (e *SSHExecutor) UploadFile(ctx context.Context, localPath, remotePath string) error {
	// TODO: 实现 SCP 或 SFTP 上传
	return fmt.Errorf("not implemented")
}

// DownloadFile 从远程服务器下载文件
func (e *SSHExecutor) DownloadFile(ctx context.Context, remotePath, localPath string) error {
	// TODO: 实现 SCP 或 SFTP 下载
	return fmt.Errorf("not implemented")
}

// ReadFile 读取远程文件内容
func (e *SSHExecutor) ReadFile(ctx context.Context, remotePath string) (string, error) {
	stdout, _, err := e.Execute(ctx, fmt.Sprintf("cat %s", remotePath))
	return stdout, err
}

// TailFile 读取远程文件尾部内容
func (e *SSHExecutor) TailFile(ctx context.Context, remotePath string, lines int) (string, error) {
	stdout, _, err := e.Execute(ctx, fmt.Sprintf("tail -n %d %s", lines, remotePath))
	return stdout, err
}

// GrepFile 在远程文件中搜索内容
func (e *SSHExecutor) GrepFile(ctx context.Context, remotePath, pattern string) (string, error) {
	stdout, _, err := e.Execute(ctx, fmt.Sprintf("grep '%s' %s", pattern, remotePath))
	return stdout, err
}

// Close 关闭连接
func (e *SSHExecutor) Close() error {
	if e.client != nil {
		return e.client.Close()
	}
	return nil
}

// IsConnected 检查是否已连接
func (e *SSHExecutor) IsConnected() bool {
	return e.client != nil
}

// Reconnect 重新连接
func (e *SSHExecutor) Reconnect() error {
	if e.client != nil {
		e.client.Close()
	}
	return e.Connect()
}
