package access

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// clusterConn 持有到首节点的连接，并由它派生子连接。
type clusterConn struct {
	entry *ssh.Client
	cfg   Config
}

// chainAddr 拼装节点的 SSH 地址。
func chainAddr(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// hostKeyCallback 根据是否提供 known_hosts 返回校验策略。
//
// 提供 known_hosts 时严格校验；未提供时退回 TOFU（接受任意主机键），
// 并返回一段提示文字，由上层写进报告，让读者知道本次连接未经身份校验。
func hostKeyCallback(knownHostsPath string) (ssh.HostKeyCallback, string, error) {
	if knownHostsPath == "" {
		return ssh.InsecureIgnoreHostKey(),
			"未配置 known_hosts，本次 SSH 连接未校验主机身份（存在中间人风险）", nil
	}
	if _, err := os.Stat(knownHostsPath); err != nil {
		return nil, "", fmt.Errorf("known_hosts %s: %w", knownHostsPath, err)
	}
	cb, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, "", fmt.Errorf("parse known_hosts %s: %w", knownHostsPath, err)
	}
	return cb, "", nil
}

// buildAuth 组装认证方式。
func buildAuth(cfg Config) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if cfg.PrivateKeyPath != "" {
		key, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("read private key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("no auth method configured")
	}
	return methods, nil
}

// Dial 建立到首节点的连接。其余节点通过 clusterConn.dialNode 派生。
func Dial(ctx context.Context, cfg Config) (*Cluster, error) {
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	auth, err := buildAuth(cfg)
	if err != nil {
		return nil, err
	}
	cfg.authMethods = auth

	if len(cfg.ExtraBinaries) > 0 {
		cfg.Policy.AllowExtraBinaries(cfg.ExtraBinaries)
	}

	hostCB, keyNote, err := hostKeyCallback(cfg.KnownHostsPath)
	if err != nil {
		return nil, err
	}
	cfg.hostKeyNote = keyNote
	cfg.hostKeyCB = hostCB

	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: hostCB,
		Timeout:         cfg.DialTimeout,
	}

	type result struct {
		conn *ssh.Client
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, derr := ssh.Dial("tcp", chainAddr(cfg.Host, cfg.Port), clientCfg)
		ch <- result{c, derr}
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("dial %s: %w", cfg.Host, ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("dial %s: %w", cfg.Host, r.err)
		}
		return newCluster(&clusterConn{entry: r.conn, cfg: cfg}), nil
	}
}

func (cc *clusterConn) Close() error {
	return cc.entry.Close()
}
