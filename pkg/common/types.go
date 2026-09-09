package common

import "time"

// Result 表示分析结果
type Result struct {
	Kind      string    `json:"kind" yaml:"kind"`           // 资源类型
	Name      string    `json:"name" yaml:"name"`           // 资源名称
	Namespace string    `json:"namespace" yaml:"namespace"` // 命名空间
	Error     []Error   `json:"error" yaml:"error"`         // 错误列表
	Details   string    `json:"details" yaml:"details"`     // 详细信息
	ParentObject string `json:"parentObject,omitempty" yaml:"parentObject,omitempty"`
}

// Error 表示发现的问题
type Error struct {
	Text      string `json:"text" yaml:"text"`           // 错误描述
	Sensitive []Sensitive `json:"sensitive,omitempty" yaml:"sensitive,omitempty"` // 敏感信息
}

// Sensitive 表示需要脱敏的信息
type Sensitive struct {
	Unmasked string `json:"unmasked" yaml:"unmasked"` // 原始值
	Masked   string `json:"masked" yaml:"masked"`     // 脱敏后的值
}

// Failure 表示带有 AI 解释的失败
type Failure struct {
	Result
	Solution   string `json:"solution" yaml:"solution"`     // AI 建议的解决方案
	Explain    string `json:"explain" yaml:"explain"`       // AI 解释
	Severity   string `json:"severity" yaml:"severity"`     // 严重程度: critical, high, medium, low
	Category   string `json:"category" yaml:"category"`     // 类别: performance, availability, security, configuration
}

// AnalysisResult 表示完整的分析结果
type AnalysisResult struct {
	Status      string    `json:"status" yaml:"status"`           // 状态: OK, Warning, Error
	Problems    int       `json:"problems" yaml:"problems"`       // 问题数量
	Results     []Result  `json:"results" yaml:"results"`         // 原始结果
	Failures    []Failure `json:"failures,omitempty" yaml:"failures,omitempty"` // 带 AI 解释的失败
	AnalyzedAt  time.Time `json:"analyzedAt" yaml:"analyzedAt"`   // 分析时间
	AnalyzeDuration string `json:"analyzeDuration" yaml:"analyzeDuration"` // 分析耗时
}

// PatrolReport 表示巡检报告
type PatrolReport struct {
	ID          string         `json:"id" yaml:"id"`                   // 报告 ID
	StartTime   time.Time      `json:"startTime" yaml:"startTime"`     // 开始时间
	EndTime     time.Time      `json:"endTime" yaml:"endTime"`         // 结束时间
	Duration    string         `json:"duration" yaml:"duration"`       // 耗时
	Status      string         `json:"status" yaml:"status"`           // 状态
	Summary     PatrolSummary  `json:"summary" yaml:"summary"`         // 汇总信息
	Findings    []Failure      `json:"findings" yaml:"findings"`       // 发现的问题
	Metrics     PatrolMetrics  `json:"metrics" yaml:"metrics"`         // 指标统计
}

// PatrolSummary 巡检汇总
type PatrolSummary struct {
	TotalIssues    int `json:"totalIssues" yaml:"totalIssues"`       // 总问题数
	CriticalIssues int `json:"criticalIssues" yaml:"criticalIssues"` // 严重问题数
	HighIssues     int `json:"highIssues" yaml:"highIssues"`         // 高风险问题数
	MediumIssues   int `json:"mediumIssues" yaml:"mediumIssues"`     // 中风险问题数
	LowIssues      int `json:"lowIssues" yaml:"lowIssues"`           // 低风险问题数
}

// PatrolMetrics 巡检指标
type PatrolMetrics struct {
	NodesChecked      int `json:"nodesChecked" yaml:"nodesChecked"`           // 检查的节点数
	PodsChecked       int `json:"podsChecked" yaml:"podsChecked"`             // 检查的 Pod 数
	PVCsChecked       int `json:"pvcsChecked" yaml:"pvcsChecked"`             // 检查的 PVC 数
	ServicesChecked   int `json:"servicesChecked" yaml:"servicesChecked"`     // 检查的服务数
	StorageChecked    int `json:"storageChecked" yaml:"storageChecked"`       // 检查的存储资源数
}

// AIProvider AI 提供商接口
type AIProvider interface {
	// GetName 返回提供商名称
	GetName() string
	// Analyze 分析问题并返回解释和解决方案
	Analyze(problem string) (explanation string, solution string, err error)
}

// Notifier 通知器接口
type Notifier interface {
	// GetName 返回通知器名称
	GetName() string
	// SendMessage 发送消息
	SendMessage(title string, content string) error
	// SendReport 发送巡检报告
	SendReport(report *PatrolReport) error
}
