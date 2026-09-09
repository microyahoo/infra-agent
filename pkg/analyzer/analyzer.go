package analyzer

import (
	"context"

	"github.com/microyahoo/infra-agent/pkg/common"
	"k8s.io/client-go/kubernetes"
)

// Analyzer 分析器接口
type Analyzer interface {
	// GetName 返回分析器名称
	GetName() string

	// GetKind 返回分析的资源类型
	GetKind() string

	// Analyze 执行分析
	Analyze(ctx context.Context, client *kubernetes.Clientset, namespace string) ([]common.Result, error)

	// IsEnabled 判断是否启用
	IsEnabled() bool

	// SetEnabled 设置启用状态
	SetEnabled(enabled bool)
}

// BaseAnalyzer 基础分析器，提供公共功能
type BaseAnalyzer struct {
	Name    string
	Kind    string
	Enabled bool
}

// GetName 返回分析器名称
func (b *BaseAnalyzer) GetName() string {
	return b.Name
}

// GetKind 返回分析的资源类型
func (b *BaseAnalyzer) GetKind() string {
	return b.Kind
}

// IsEnabled 判断是否启用
func (b *BaseAnalyzer) IsEnabled() bool {
	return b.Enabled
}

// SetEnabled 设置启用状态
func (b *BaseAnalyzer) SetEnabled(enabled bool) {
	b.Enabled = enabled
}

// AnalyzerRegistry 分析器注册表
type AnalyzerRegistry struct {
	analyzers map[string]Analyzer
}

// NewAnalyzerRegistry 创建分析器注册表
func NewAnalyzerRegistry() *AnalyzerRegistry {
	return &AnalyzerRegistry{
		analyzers: make(map[string]Analyzer),
	}
}

// Register 注册分析器
func (r *AnalyzerRegistry) Register(analyzer Analyzer) {
	r.analyzers[analyzer.GetName()] = analyzer
}

// Get 获取分析器
func (r *AnalyzerRegistry) Get(name string) (Analyzer, bool) {
	analyzer, ok := r.analyzers[name]
	return analyzer, ok
}

// List 列出所有分析器
func (r *AnalyzerRegistry) List() []Analyzer {
	var list []Analyzer
	for _, analyzer := range r.analyzers {
		list = append(list, analyzer)
	}
	return list
}

// GetEnabled 获取所有启用的分析器
func (r *AnalyzerRegistry) GetEnabled() []Analyzer {
	var enabled []Analyzer
	for _, analyzer := range r.analyzers {
		if analyzer.IsEnabled() {
			enabled = append(enabled, analyzer)
		}
	}
	return enabled
}
