package access

import "golang.org/x/crypto/ssh"

// Cluster 是接入层对外的唯一入口。
// 本任务只提供构造与连接骨架；命令执行、能力探测、拓扑发现在后续任务中补齐。
type Cluster struct {
	conn    *clusterConn
	clients map[string]*ssh.Client
}

func newCluster(cc *clusterConn) *Cluster {
	return &Cluster{conn: cc, clients: map[string]*ssh.Client{}}
}

// Close 关闭所有派生连接以及入口连接。
func (c *Cluster) Close() error {
	for _, cl := range c.clients {
		if cl != c.conn.entry {
			cl.Close()
		}
	}
	return c.conn.Close()
}
