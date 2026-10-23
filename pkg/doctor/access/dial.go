package access

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"

	"golang.org/x/crypto/ssh"
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

	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
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

// dialNode 经首节点建立到目标节点的连接，复用首节点的免密配置。
// 首节点自身直接复用 entry。
func (cc *clusterConn) dialNode(ctx context.Context, host string) (*ssh.Client, error) {
	if host == cc.cfg.Host {
		return cc.entry, nil
	}

	clientCfg := &ssh.ClientConfig{
		User:            cc.cfg.User,
		Auth:            cc.cfg.authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         cc.cfg.DialTimeout,
	}

	type result struct {
		conn *ssh.Client
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		raw, derr := cc.entry.Dial("tcp", chainAddr(host, cc.cfg.Port))
		if derr != nil {
			ch <- result{nil, derr}
			return
		}
		// v0.16.0 起 NewClientConn 直接返回握手后的 Conn 与通道，交给 NewClient 组装。
		conn, chans, reqs, cerr := ssh.NewClientConn(raw, chainAddr(host, cc.cfg.Port), clientCfg)
		if cerr != nil {
			raw.Close()
			ch <- result{nil, cerr}
			return
		}
		ch <- result{ssh.NewClient(conn, chans, reqs), nil}
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("chain to %s: %w", host, ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("chain to %s: %w", host, r.err)
		}
		return r.conn, nil
	}
}

func (cc *clusterConn) Close() error {
	return cc.entry.Close()
}
