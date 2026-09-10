# Infra Agent 大规模多集群架构设计

## 📊 规模需求

- **集群数量**: 10+ 套 Kubernetes 集群
- **节点规模**: 数千台物理服务器
- **存储节点**: 数百台 Ceph 存储服务器
- **并发要求**: 同时监控多个集群
- **性能要求**: 秒级问题发现，分钟级诊断

---

## 🏗️ 架构演进

### 当前架构（单集群模式）
```
infra-agent (CLI)
    ↓
单个 K8s 集群
    ↓
有限的存储节点
```

**局限性**：
- ❌ 只能顺序处理一个集群
- ❌ 无法并发监控多个集群
- ❌ 扩展性差
- ❌ 无法处理大规模节点

### 升级架构（企业级多集群）

```
┌─────────────────────────────────────────────────────────────┐
│                  Infra Agent Platform                        │
│                    (中心化平台)                              │
├─────────────────────────────────────────────────────────────┤
│  API Gateway / Load Balancer                                │
├─────────────────────────────────────────────────────────────┤
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │ Agent Manager│  │  Scheduler   │  │ AI Service   │      │
│  │  集群管理    │  │  任务调度    │  │  AI 诊断池   │      │
│  └──────────────┘  └──────────────┘  └──────────────┘      │
├─────────────────────────────────────────────────────────────┤
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │ Data Store   │  │  Queue       │  │  Cache       │      │
│  │  PostgreSQL  │  │  Redis/NATS  │  │  Redis       │      │
│  └──────────────┘  └──────────────┘  └──────────────┘      │
└─────────────────────────────────────────────────────────────┘
                           ↓
        ┌──────────────────┼──────────────────┐
        ↓                  ↓                  ↓
┌──────────────┐  ┌──────────────┐  ┌──────────────┐
│  Cluster 1   │  │  Cluster 2   │  │  Cluster N   │
│  Agent       │  │  Agent       │  │  Agent       │
│  (DaemonSet) │  │  (DaemonSet) │  │  (DaemonSet) │
└──────────────┘  └──────────────┘  └──────────────┘
        ↓                  ↓                  ↓
    100+ nodes        500+ nodes       1000+ nodes
     + Storage         + Storage        + Storage
```

---

## 🎯 核心设计要点

### 1. 分布式 Agent 架构

#### 1.1 集群内 Agent（边缘采集）

```yaml
# deployments/kubernetes/daemonset.yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: infra-agent
  namespace: kube-system
spec:
  selector:
    matchLabels:
      app: infra-agent
  template:
    metadata:
      labels:
        app: infra-agent
    spec:
      hostNetwork: true
      hostPID: true
      containers:
      - name: agent
        image: infra-agent:latest
        env:
        - name: CLUSTER_ID
          value: "prod-cluster-1"
        - name: AGENT_MODE
          value: "edge"  # 边缘模式
        - name: CONTROL_PLANE_URL
          value: "https://infra-platform.company.com"
        - name: REPORT_INTERVAL
          value: "30s"
        volumeMounts:
        - name: docker-socket
          mountPath: /var/run/docker.sock
        - name: logs
          mountPath: /var/log
        - name: ceph-logs
          mountPath: /var/log/ceph
```

**职责**：
- 采集本节点的指标和日志
- 执行轻量级诊断命令
- 上报数据到中心平台
- 接收中心平台的诊断指令

#### 1.2 中心控制平台

```go
// pkg/platform/manager.go

type ClusterManager struct {
    clusters      map[string]*ClusterConfig
    agentRegistry *AgentRegistry
    scheduler     *TaskScheduler
    dataStore     *DataStore
    queue         *MessageQueue
}

type ClusterConfig struct {
    ID              string
    Name            string
    KubeConfig      string
    StorageServers  []StorageServerInfo
    NodeCount       int
    AgentEndpoints  []string
    Priority        int  // 优先级
    Region          string
}

// 注册集群
func (m *ClusterManager) RegisterCluster(config *ClusterConfig) error {
    // 验证连接
    client, err := kubernetes.NewForConfig(config.KubeConfig)
    if err != nil {
        return err
    }
    
    // 部署 Agent DaemonSet
    if err := m.deployAgents(client, config); err != nil {
        return err
    }
    
    // 发现存储节点
    storageNodes, err := m.discoverStorageNodes(config)
    if err != nil {
        return err
    }
    config.StorageServers = storageNodes
    
    m.clusters[config.ID] = config
    return nil
}
```

### 2. 任务调度系统

#### 2.1 分布式任务调度

```go
// pkg/platform/scheduler.go

type TaskScheduler struct {
    queue         *PriorityQueue
    workers       *WorkerPool
    rateLimiter   *RateLimiter
}

type AnalysisTask struct {
    ID          string
    ClusterID   string
    Type        TaskType  // 全量巡检、快速检查、深度诊断
    Priority    int
    Timeout     time.Duration
    RetryCount  int
    Callback    string
}

// 智能调度
func (s *TaskScheduler) Schedule(task *AnalysisTask) error {
    // 1. 检查集群负载
    cluster := s.getCluster(task.ClusterID)
    if cluster.Load > 0.8 {
        // 延迟调度
        return s.delayTask(task, 30*time.Second)
    }
    
    // 2. 分配到合适的 Worker
    worker := s.workers.GetAvailableWorker(cluster.Region)
    if worker == nil {
        return s.queue.Push(task)
    }
    
    // 3. 执行任务
    go s.executeTask(worker, task)
    
    return nil
}

// 批量调度（多集群并发）
func (s *TaskScheduler) ScheduleBatch(tasks []*AnalysisTask) error {
    // 按集群分组
    tasksByCluster := groupTasksByCluster(tasks)
    
    // 并发执行
    var wg sync.WaitGroup
    semaphore := make(chan struct{}, 10) // 最多 10 个并发
    
    for clusterID, clusterTasks := range tasksByCluster {
        wg.Add(1)
        semaphore <- struct{}{}
        
        go func(cid string, tasks []*AnalysisTask) {
            defer wg.Done()
            defer func() { <-semaphore }()
            
            s.executeClusterTasks(cid, tasks)
        }(clusterID, clusterTasks)
    }
    
    wg.Wait()
    return nil
}
```

#### 2.2 节点分片处理

```go
// pkg/platform/shard.go

type ShardManager struct {
    shardSize int  // 每个分片的节点数
}

// 将大量节点分片处理
func (m *ShardManager) ShardNodes(clusterID string, nodes []string) [][]string {
    // 按 shardSize 分片
    shards := make([][]string, 0)
    
    for i := 0; i < len(nodes); i += m.shardSize {
        end := i + m.shardSize
        if end > len(nodes) {
            end = len(nodes)
        }
        shards = append(shards, nodes[i:end])
    }
    
    return shards
}

// 并行处理分片
func (m *ShardManager) ProcessShards(clusterID string, shards [][]string, 
    processor func([]string) error) error {
    
    var wg sync.WaitGroup
    errChan := make(chan error, len(shards))
    
    // 限制并发数
    semaphore := make(chan struct{}, 5)
    
    for _, shard := range shards {
        wg.Add(1)
        semaphore <- struct{}{}
        
        go func(nodes []string) {
            defer wg.Done()
            defer func() { <-semaphore }()
            
            if err := processor(nodes); err != nil {
                errChan <- err
            }
        }(shard)
    }
    
    wg.Wait()
    close(errChan)
    
    // 收集错误
    var errors []error
    for err := range errChan {
        errors = append(errors, err)
    }
    
    if len(errors) > 0 {
        return fmt.Errorf("shard processing errors: %v", errors)
    }
    
    return nil
}
```

### 3. 数据存储和缓存

#### 3.1 时序数据库（指标）

```go
// pkg/platform/storage/timeseries.go

type TimeSeriesStore struct {
    client *prometheus.Client  // 或 InfluxDB、VictoriaMetrics
}

// 存储节点指标
func (s *TimeSeriesStore) WriteMetrics(clusterID string, metrics []*Metric) error {
    // 批量写入
    batch := make([]*prompb.TimeSeries, len(metrics))
    
    for i, m := range metrics {
        batch[i] = &prompb.TimeSeries{
            Labels: []prompb.Label{
                {Name: "cluster", Value: clusterID},
                {Name: "node", Value: m.NodeName},
                {Name: "metric", Value: m.Name},
            },
            Samples: []prompb.Sample{
                {
                    Value:     m.Value,
                    Timestamp: time.Now().Unix(),
                },
            },
        }
    }
    
    return s.client.Write(batch)
}
```

#### 3.2 关系数据库（配置和结果）

```sql
-- schema.sql

-- 集群表
CREATE TABLE clusters (
    id VARCHAR(64) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    region VARCHAR(64),
    node_count INT,
    storage_node_count INT,
    status VARCHAR(32),
    created_at TIMESTAMP,
    updated_at TIMESTAMP
);

-- 节点表
CREATE TABLE nodes (
    id VARCHAR(64) PRIMARY KEY,
    cluster_id VARCHAR(64) REFERENCES clusters(id),
    name VARCHAR(255),
    ip VARCHAR(64),
    role VARCHAR(32),  -- master, worker, storage
    status VARCHAR(32),
    last_seen TIMESTAMP,
    INDEX idx_cluster_id (cluster_id),
    INDEX idx_last_seen (last_seen)
);

-- 分析结果表（分区表，按时间分区）
CREATE TABLE analysis_results (
    id BIGSERIAL PRIMARY KEY,
    cluster_id VARCHAR(64),
    node_name VARCHAR(255),
    resource_kind VARCHAR(64),
    resource_name VARCHAR(255),
    severity VARCHAR(32),
    error_message TEXT,
    diagnosis TEXT,
    solution TEXT,
    created_at TIMESTAMP
) PARTITION BY RANGE (created_at);

-- 按月分区
CREATE TABLE analysis_results_2024_01 PARTITION OF analysis_results
    FOR VALUES FROM ('2024-01-01') TO ('2024-02-01');
```

#### 3.3 缓存层（Redis）

```go
// pkg/platform/storage/cache.go

type CacheStore struct {
    redis *redis.Client
}

// 缓存集群状态
func (c *CacheStore) CacheClusterStatus(clusterID string, status *ClusterStatus) error {
    key := fmt.Sprintf("cluster:status:%s", clusterID)
    data, _ := json.Marshal(status)
    
    return c.redis.Set(context.Background(), key, data, 5*time.Minute).Err()
}

// 缓存节点健康状态
func (c *CacheStore) CacheNodeHealth(clusterID, nodeID string, health *NodeHealth) error {
    key := fmt.Sprintf("node:health:%s:%s", clusterID, nodeID)
    data, _ := json.Marshal(health)
    
    return c.redis.Set(context.Background(), key, data, 1*time.Minute).Err()
}

// 批量获取节点状态
func (c *CacheStore) GetNodesHealthBatch(clusterID string, nodeIDs []string) (map[string]*NodeHealth, error) {
    pipe := c.redis.Pipeline()
    
    cmds := make(map[string]*redis.StringCmd)
    for _, nodeID := range nodeIDs {
        key := fmt.Sprintf("node:health:%s:%s", clusterID, nodeID)
        cmds[nodeID] = pipe.Get(context.Background(), key)
    }
    
    _, err := pipe.Exec(context.Background())
    if err != nil && err != redis.Nil {
        return nil, err
    }
    
    result := make(map[string]*NodeHealth)
    for nodeID, cmd := range cmds {
        val, err := cmd.Result()
        if err == redis.Nil {
            continue
        }
        
        var health NodeHealth
        json.Unmarshal([]byte(val), &health)
        result[nodeID] = &health
    }
    
    return result, nil
}
```

### 4. 消息队列（异步处理）

```go
// pkg/platform/queue/queue.go

type MessageQueue struct {
    nats *nats.Conn
}

// 发布分析任务
func (q *MessageQueue) PublishAnalysisTask(task *AnalysisTask) error {
    data, _ := json.Marshal(task)
    
    subject := fmt.Sprintf("analysis.%s.%s", task.ClusterID, task.Type)
    return q.nats.Publish(subject, data)
}

// 订阅任务（Worker）
func (q *MessageQueue) SubscribeAnalysisTasks(clusterID string, handler func(*AnalysisTask)) error {
    subject := fmt.Sprintf("analysis.%s.*", clusterID)
    
    _, err := q.nats.Subscribe(subject, func(m *nats.Msg) {
        var task AnalysisTask
        json.Unmarshal(m.Data, &task)
        handler(&task)
    })
    
    return err
}

// 工作队列模式（负载均衡）
func (q *MessageQueue) QueueGroup(clusterID string, group string, handler func(*AnalysisTask)) error {
    subject := fmt.Sprintf("analysis.%s.*", clusterID)
    
    // QueueSubscribe 自动负载均衡
    _, err := q.nats.QueueSubscribe(subject, group, func(m *nats.Msg) {
        var task AnalysisTask
        json.Unmarshal(m.Data, &task)
        handler(&task)
    })
    
    return err
}
```

### 5. AI 服务池（避免 API 限流）

```go
// pkg/platform/ai/pool.go

type AIServicePool struct {
    providers []AIProvider
    current   int
    mu        sync.Mutex
    
    // 限流器
    rateLimiter *rate.Limiter
}

func NewAIServicePool(configs []AIProviderConfig) *AIServicePool {
    providers := make([]AIProvider, len(configs))
    
    for i, config := range configs {
        switch config.Type {
        case "openai":
            providers[i] = NewOpenAIProvider(config.APIKey, config.Model)
        case "anthropic":
            providers[i] = NewAnthropicProvider(config.APIKey, config.Model)
        }
    }
    
    return &AIServicePool{
        providers:   providers,
        rateLimiter: rate.NewLimiter(rate.Every(time.Second), 10), // 每秒 10 个请求
    }
}

// 轮询选择 Provider（避免单个 API Key 限流）
func (p *AIServicePool) GetProvider() AIProvider {
    p.mu.Lock()
    defer p.mu.Unlock()
    
    provider := p.providers[p.current]
    p.current = (p.current + 1) % len(p.providers)
    
    return provider
}

// 带重试的分析
func (p *AIServicePool) AnalyzeWithRetry(problem string, maxRetries int) (string, string, error) {
    // 等待限流
    p.rateLimiter.Wait(context.Background())
    
    var lastErr error
    
    for i := 0; i < maxRetries; i++ {
        provider := p.GetProvider()
        
        explanation, solution, err := provider.Analyze(problem)
        if err == nil {
            return explanation, solution, nil
        }
        
        lastErr = err
        
        // 如果是限流错误，切换到下一个 Provider
        if isRateLimitError(err) {
            time.Sleep(time.Second)
            continue
        }
        
        // 其他错误，指数退避
        time.Sleep(time.Duration(i+1) * time.Second)
    }
    
    return "", "", fmt.Errorf("all retries failed: %v", lastErr)
}
```

---

## 💡 实际部署架构

### 部署拓扑

```
┌────────────────────────────────────────────────────────────┐
│  Load Balancer (HA Proxy / Nginx)                          │
│  - infra-platform.company.com                              │
└────────────────────────────────────────────────────────────┘
                        ↓
┌────────────────────────────────────────────────────────────┐
│  Control Plane Cluster (3 nodes)                           │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐    │
│  │  API Server  │  │  Scheduler   │  │  AI Pool     │    │
│  │  (stateless) │  │  (stateless) │  │  (stateless) │    │
│  └──────────────┘  └──────────────┘  └──────────────┘    │
└────────────────────────────────────────────────────────────┘
                        ↓
┌────────────────────────────────────────────────────────────┐
│  Data Layer                                                 │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐    │
│  │  PostgreSQL  │  │  Redis       │  │  NATS/Kafka  │    │
│  │  (Primary+   │  │  (Cluster)   │  │  (Cluster)   │    │
│  │   Replicas)  │  │              │  │              │    │
│  └──────────────┘  └──────────────┘  └──────────────┘    │
└────────────────────────────────────────────────────────────┘
                        ↓
        ┌───────────────┼───────────────┐
        ↓               ↓               ↓
┌──────────────┐ ┌──────────────┐ ┌──────────────┐
│ Cluster 1    │ │ Cluster 2    │ │ Cluster N    │
│ (Beijing)    │ │ (Shanghai)   │ │ (Guangzhou)  │
│              │ │              │ │              │
│ Agent: 50    │ │ Agent: 200   │ │ Agent: 500   │
│ Nodes: 100   │ │ Nodes: 500   │ │ Nodes: 1000  │
│ Storage: 20  │ │ Storage: 50  │ │ Storage: 100 │
└──────────────┘ └──────────────┘ └──────────────┘
```

### 资源规划

#### Control Plane

| 组件 | 副本数 | CPU | 内存 | 说明 |
|------|--------|-----|------|------|
| API Server | 3 | 4核 | 8GB | 无状态，可水平扩展 |
| Scheduler | 3 | 2核 | 4GB | 主从模式 |
| AI Pool | 5+ | 2核 | 4GB | 根据 API 调用量扩展 |
| PostgreSQL | 1主2从 | 8核 | 32GB | 持久化存储 |
| Redis | 3节点集群 | 4核 | 16GB | 缓存和队列 |
| NATS | 3节点集群 | 2核 | 4GB | 消息队列 |

#### Edge Agents

| 规模 | Agent 模式 | 资源限制 | 采集间隔 |
|------|-----------|----------|----------|
| <100 节点 | DaemonSet | 100m CPU, 256MB | 30s |
| 100-500 节点 | DaemonSet | 200m CPU, 512MB | 60s |
| >500 节点 | 采样模式 | 100m CPU, 256MB | 90s |

---

## 🚀 使用示例

### 1. 注册多个集群

```bash
# 注册集群 1（北京）
infra-agent cluster add \
  --id prod-bj-01 \
  --name "北京生产集群" \
  --kubeconfig ~/.kube/prod-bj-01.yaml \
  --region beijing \
  --priority 1

# 注册集群 2（上海）
infra-agent cluster add \
  --id prod-sh-01 \
  --name "上海生产集群" \
  --kubeconfig ~/.kube/prod-sh-01.yaml \
  --region shanghai \
  --priority 1

# 列出所有集群
infra-agent cluster list
```

### 2. 多集群并发巡检

```bash
# 巡检所有集群
infra-agent patrol start-all --interval 30m

# 巡检特定区域
infra-agent patrol start --region beijing --interval 15m

# 巡检特定集群
infra-agent patrol start --cluster prod-bj-01 --interval 10m
```

### 3. 大规模节点诊断

```bash
# 分片处理 1000 个节点（每批 50 个）
infra-agent analyze \
  --cluster prod-sh-01 \
  --filter Node \
  --shard-size 50 \
  --parallel 10 \
  --explain --auto-diagnose

# 查看进度
infra-agent task status --cluster prod-sh-01
```

### 4. 多集群查询

```bash
# 查询所有集群的问题
infra-agent query \
  --severity critical \
  --last 1h \
  --all-clusters

# 查询特定集群的存储问题
infra-agent query \
  --cluster prod-bj-01 \
  --filter RookCeph,RemoteStorage \
  --last 24h
```

---

## 📊 性能优化

### 1. 采集优化

```yaml
# config/platform.yaml
collection:
  # 采样策略
  sampling:
    enabled: true
    rate: 0.1  # 10% 采样率（大规模集群）
    
  # 增量采集
  incremental: true
  
  # 批量处理
  batch:
    size: 100
    timeout: 30s
```

### 2. 并发控制

```go
// 动态调整并发数
func (s *Scheduler) GetOptimalConcurrency(clusterSize int) int {
    switch {
    case clusterSize < 100:
        return 10
    case clusterSize < 500:
        return 20
    case clusterSize < 1000:
        return 30
    default:
        return 50
    }
}
```

### 3. 缓存策略

- **热数据**: Redis，1-5 分钟
- **温数据**: PostgreSQL + 内存缓存，1 小时
- **冷数据**: 对象存储（S3），归档

---

## 🎯 总结

大规模多集群架构的关键点：

1. ✅ **分布式架构** - 边缘采集 + 中心处理
2. ✅ **任务调度** - 分片、并发、优先级
3. ✅ **数据分层** - 时序数据库 + 关系数据库 + 缓存
4. ✅ **消息队列** - 异步处理，解耦
5. ✅ **AI 服务池** - 多 Provider，避免限流
6. ✅ **监控告警** - 实时监控平台健康
7. ✅ **自动扩展** - 根据负载自动扩容

需要我详细实现哪个部分？
