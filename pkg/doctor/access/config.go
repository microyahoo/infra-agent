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
	DialTimeout    time.Duration
	MaxOutputBytes int
	MaxConcurrency int
	Policy         *Policy

	// authMethods 由 Dial 内部填充，供链式连接复用
	authMethods []ssh.AuthMethod
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
