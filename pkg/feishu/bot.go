package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/microyahoo/infra-agent/pkg/doctor/llm"
)

// LLMConfig 是机器人使用的模型配置。
type LLMConfig struct {
	BaseURL    string
	APIKey     string
	Model      string
	MaxTokens  int
	MaxRetries int
}

// Config 是飞书机器人的配置。
type Config struct {
	AppID     string
	AppSecret string
	// VerificationToken / EncryptKey 在长连接模式下可留空。
	VerificationToken string
	EncryptKey        string

	// Cluster 是默认集群；Clusters 按名称提供多套集群。
	Cluster  ClusterConfig
	Clusters map[string]ClusterConfig

	// ChatClusters 指定某个群默认查哪套集群（chat_id → 集群名）。
	ChatClusters map[string]string

	LLM      LLMConfig
	NewModel func() llm.ChatModel // 测试注入

	SourceDir        string
	MaxRounds        int
	MaxCallsPerRound int
	MaxCallsPerTurn  int
	IdleTimeout      time.Duration
}

// Bot 是飞书长连接机器人：在群里被 @ 时，按消息内容调查集群并回复。
type Bot struct {
	cfg      Config
	api      *lark.Client
	sessions *SessionManager

	mu   sync.Mutex
	seen map[string]time.Time // 已处理的消息 ID，防飞书重投
}

// New 创建机器人。参数缺失时返回错误，便于启动时立刻发现配置问题。
func New(cfg Config) (*Bot, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, fmt.Errorf("缺少 app_id / app_secret")
	}
	if cfg.Cluster.Host == "" && len(cfg.Clusters) == 0 {
		return nil, fmt.Errorf("没有任何集群配置")
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}
	return &Bot{
		cfg:      cfg,
		api:      lark.NewClient(cfg.AppID, cfg.AppSecret),
		sessions: newSessionManager(cfg),
		seen:     map[string]time.Time{},
	}, nil
}

// Run 启动长连接，直到 ctx 结束。
//
// 长连接由本进程主动连飞书服务器，不需要公网地址或回调 URL。
func (b *Bot) Run(ctx context.Context) error {
	handler := dispatcher.NewEventDispatcher(b.cfg.VerificationToken, b.cfg.EncryptKey).
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			b.handle(ctx, event)
			return nil
		})

	cli := larkws.NewClient(b.cfg.AppID, b.cfg.AppSecret,
		larkws.WithEventHandler(handler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)
	cli.SetOnReady(func() { log.Printf("飞书长连接已就绪") })
	cli.SetOnError(func(err error) { log.Printf("飞书长连接错误: %v", err) })

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.sessions.cleanupIdle(b.cfg.IdleTimeout)
			}
		}
	}()

	err := cli.Start(ctx)
	b.sessions.Close()
	return err
}

// handle 处理一条群消息。
//
// 立即返回、异步调查：长连接的事件处理不能被一次几分钟的诊断阻塞。
func (b *Bot) handle(ctx context.Context, event *larkim.P2MessageReceiveV1) {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return
	}
	msg := event.Event.Message

	// 只处理真人发的消息；忽略机器人自己的回复，避免自问自答。
	if event.Event.Sender != nil && deref(event.Event.Sender.SenderType) == "app" {
		return
	}
	if deref(msg.MessageType) != "text" {
		b.reply(ctx, deref(msg.MessageId), "目前只支持文本提问。")
		return
	}
	if !b.markSeen(deref(msg.MessageId)) {
		return // 飞书重投
	}

	text := extractText(deref(msg.Content))
	// 去掉 @ 提及留下的占位符（@_user_1 等）
	for _, m := range msg.Mentions {
		if m != nil && m.Key != nil {
			text = strings.ReplaceAll(text, *m.Key, "")
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		b.reply(ctx, deref(msg.MessageId), "想查什么？例如：分析一下集群为啥 warn。")
		return
	}

	chatID := deref(msg.ChatId)
	msgID := deref(msg.MessageId)
	clusterName := b.clusterForChat(chatID, text)
	if clusterName != "" {
		text = stripClusterPrefix(text, clusterName)
	}

	go func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		entry, err := b.sessions.get(cctx, chatID, clusterName)
		if err != nil {
			b.reply(cctx, msgID, "无法接入集群："+err.Error())
			return
		}

		// 同一个群内串行：用户连续发问时排队，而不是让两个调查交错。
		entry.mu.Lock()
		defer entry.mu.Unlock()

		reply, err := entry.agent.Send(cctx, text)
		if err != nil {
			b.reply(cctx, msgID, "调查中断："+err.Error())
			return
		}
		b.reply(cctx, msgID, reply)
	}()
}

// clusterForChat 决定这条消息查哪套集群：
// 群绑定优先，其次是消息里的 "集群名:问题" 形式，最后用默认集群。
func (b *Bot) clusterForChat(chatID, text string) string {
	if name, ok := b.cfg.ChatClusters[chatID]; ok {
		return name
	}
	if i := strings.Index(text, ":"); i > 0 && i < 40 {
		candidate := strings.TrimSpace(text[:i])
		if _, ok := b.cfg.Clusters[candidate]; ok {
			return candidate
		}
	}
	return ""
}

// stripClusterPrefix 去掉 "集群名:" 前缀。
//
// 只剥离明确匹配该集群名的前缀，避免把 "http://..." 这类内容误截。
func stripClusterPrefix(text, clusterName string) string {
	trimmed := strings.TrimSpace(text)
	prefix := clusterName + ":"
	if strings.HasPrefix(trimmed, prefix) {
		return strings.TrimSpace(trimmed[len(prefix):])
	}
	return text
}

// markSeen 记录已处理的消息；返回 false 表示重复投递。
func (b *Bot) markSeen(id string) bool {
	if id == "" {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.seen[id]; ok {
		return false
	}
	b.seen[id] = time.Now()
	// 只保留最近一小时，避免无限增长
	if len(b.seen) > 2000 {
		cutoff := time.Now().Add(-time.Hour)
		for k, t := range b.seen {
			if t.Before(cutoff) {
				delete(b.seen, k)
			}
		}
	}
	return true
}

// reply 以引用原消息的方式回复，保持群里的上下文清晰。
func (b *Bot) reply(ctx context.Context, messageID, text string) {
	if messageID == "" || strings.TrimSpace(text) == "" {
		return
	}
	content, _ := json.Marshal(map[string]string{"text": clip(text, 100000)})
	body := &larkim.ReplyMessageReqBody{
		Content: larkcore.StringPtr(string(content)),
		MsgType: larkcore.StringPtr("text"),
	}
	_, err := b.api.Im.V1.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
		MessageId(messageID).Body(body).Build())
	if err != nil {
		log.Printf("回复消息失败: %v", err)
	}
}

// extractText 从飞书消息 content（JSON）里取文本。
func extractText(content string) string {
	var v struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return content
	}
	return v.Text
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// clip 按 rune 截断，避免超出飞书单条消息限制。
func clip(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "\n…（内容过长已截断）"
}
