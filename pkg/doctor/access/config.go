package access

import (
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
)

// Config 接入层配置。
type Config struct {
	Host           string
	Port           int
	User           string
	Password       string
	PrivateKeyPath string
	KnownHostsPath string
	CommandTimeout time.Duration
	// ProbeTimeout 用于能力探测/拓扑发现这类「快速查询」。
	// 探测阶段的命令本该秒回，用和诊断命令一样的 30s 只会让失败场景
	// 白白等待成倍时间（探测最多十几次命令）。
	ProbeTimeout   time.Duration
	DialTimeout    time.Duration
	MaxOutputBytes int
	MaxConcurrency int
	Policy         *Policy

	// OnProgress 可选：接入层在耗时操作前后调用它，便于 CLI 展示进度。
	// 只用于显示，不参与任何决策。
	OnProgress func(string)

	// authMethods 由 Dial 内部填充，供链式连接复用
	authMethods []ssh.AuthMethod
	// hostKeyCB 由 Dial 内部填充，链式连接复用同一校验策略
	hostKeyCB ssh.HostKeyCallback
	// hostKeyNote 记录主机键校验的放宽情况，写入报告警示
	hostKeyNote string
}

func (c *Config) applyDefaults() {
	if c.Port == 0 {
		c.Port = 22
	}
	if c.User == "" {
		c.User = "root"
	}
	if c.CommandTimeout == 0 {
		c.CommandTimeout = 30 * time.Second
	}
	if c.ProbeTimeout == 0 {
		c.ProbeTimeout = 8 * time.Second
	}
	if c.ProbeTimeout > c.CommandTimeout {
		c.ProbeTimeout = c.CommandTimeout
	}
	if c.DialTimeout == 0 {
		c.DialTimeout = 10 * time.Second
	}
	if c.MaxOutputBytes == 0 {
		c.MaxOutputBytes = 256 * 1024
	}
	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = 8
	}
	if c.Policy == nil {
		c.Policy = DefaultPolicy()
	}
}

func (c *Config) validate() error {
	if c.Host == "" {
		return fmt.Errorf("ssh host is required")
	}
	if c.PrivateKeyPath == "" && c.Password == "" {
		return fmt.Errorf("one of private_key or password is required")
	}
	return nil
}
