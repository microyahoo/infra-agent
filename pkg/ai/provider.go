package ai

import (
	"fmt"
	"sync"

	"github.com/microyahoo/infra-agent/pkg/common"
)

// ProviderRegistry AI 提供商注册表
type ProviderRegistry struct {
	providers map[string]common.AIProvider
	mu        sync.RWMutex
	default_  string
}

// NewProviderRegistry 创建 AI 提供商注册表
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers: make(map[string]common.AIProvider),
	}
}

// Register 注册 AI 提供商
func (r *ProviderRegistry) Register(provider common.AIProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[provider.GetName()] = provider
}

// Get 获取 AI 提供商
func (r *ProviderRegistry) Get(name string) (common.AIProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	provider, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("AI provider %s not found", name)
	}
	return provider, nil
}

// GetDefault 获取默认 AI 提供商
func (r *ProviderRegistry) GetDefault() (common.AIProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.default_ == "" {
		return nil, fmt.Errorf("no default AI provider configured")
	}

	provider, ok := r.providers[r.default_]
	if !ok {
		return nil, fmt.Errorf("default AI provider %s not found", r.default_)
	}
	return provider, nil
}

// SetDefault 设置默认 AI 提供商
func (r *ProviderRegistry) SetDefault(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.providers[name]; !ok {
		return fmt.Errorf("AI provider %s not found", name)
	}

	r.default_ = name
	return nil
}

// List 列出所有 AI 提供商
func (r *ProviderRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var names []string
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}
