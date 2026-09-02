package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

const (
	ProviderUnconfigured = "unconfigured"
	ProviderConnecting   = "connecting"
	ProviderConnected    = "connected"
	ProviderDisconnected = "disconnected"
	ProviderStopping     = "stopping"
)

var ErrProviderMessageIgnored = errors.New("platform: provider message ignored")

type BotConfig struct {
	TelegramUsername string
	TelegramToken    string
	WeComBotID       string
	WeComSecret      string
}

func LoadBotConfig(getenv func(string) string) BotConfig {
	if getenv == nil {
		getenv = os.Getenv
	}
	return BotConfig{
		TelegramUsername: getenv("TRPC_TELEGRAM_BOT_USERNAME"),
		TelegramToken:    getenv("TRPC_TELEGRAM_BOT_TOKEN"),
		WeComBotID:       getenv("TRPC_WECOM_BOT_ID"),
		WeComSecret:      getenv("TRPC_WECOM_BOT_SECRET"),
	}
}

type BotRoute struct {
	Provider         string `json:"provider"`
	ExternalSubject  string `json:"external_subject"`
	TenantID         string `json:"tenant_id"`
	AppID            string `json:"app_id"`
	ConversationType string `json:"conversation_type"`
}

type BotTenantAllowlist struct {
	mu     sync.RWMutex
	routes map[string]BotRoute
}

func NewBotTenantAllowlist() *BotTenantAllowlist {
	return &BotTenantAllowlist{routes: make(map[string]BotRoute)}
}

func botRouteKey(provider, subject string) string { return provider + "\x00" + subject }

func providerSessionID(provider, account, subject string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + account + "\x00" + subject))
	return "im-" + hex.EncodeToString(sum[:12])
}

func (a *BotTenantAllowlist) Upsert(route BotRoute) error {
	if route.Provider == "" || route.ExternalSubject == "" || route.TenantID == "" || route.AppID == "" {
		return errors.New("platform: invalid bot route")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := botRouteKey(route.Provider, route.ExternalSubject)
	if previous, ok := a.routes[key]; ok && (previous.TenantID != route.TenantID || previous.AppID != route.AppID) {
		return errors.New("platform: ambiguous bot route")
	}
	a.routes[key] = route
	return nil
}

func (a *BotTenantAllowlist) Resolve(provider, subject string) (BotRoute, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	route, ok := a.routes[botRouteKey(provider, subject)]
	return route, ok
}

func (a *BotTenantAllowlist) List() []BotRoute {
	a.mu.RLock()
	defer a.mu.RUnlock()
	items := make([]BotRoute, 0, len(a.routes))
	for _, route := range a.routes {
		items = append(items, route)
	}
	sort.Slice(items, func(i, j int) bool {
		return botRouteKey(items[i].Provider, items[i].ExternalSubject) < botRouteKey(items[j].Provider, items[j].ExternalSubject)
	})
	return items
}

func (a *BotTenantAllowlist) Delete(provider, subject string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.routes, botRouteKey(provider, subject))
}

type BotStatus struct {
	Provider string `json:"provider"`
	Status   string `json:"status"`
	LastErr  string `json:"last_error,omitempty"`
}

type ProviderRuntime struct {
	config    BotConfig
	routes    *BotTenantAllowlist
	client    *http.Client
	mu        sync.RWMutex
	statuses  map[string]BotStatus
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	wecomConn *websocket.Conn
	started   bool
	processor func(context.Context, string, string, []byte) error
}

func NewProviderRuntime(config BotConfig, routes *BotTenantAllowlist, processor func(context.Context, string, string, []byte) error) *ProviderRuntime {
	if routes == nil {
		routes = NewBotTenantAllowlist()
	}
	return &ProviderRuntime{config: config, routes: routes, client: http.DefaultClient, statuses: make(map[string]BotStatus), processor: processor}
}

func (p *ProviderRuntime) Statuses() []BotStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	items := make([]BotStatus, 0, len(p.statuses))
	for _, status := range p.statuses {
		items = append(items, status)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Provider < items[j].Provider })
	return items
}

func (p *ProviderRuntime) Routes() *BotTenantAllowlist { return p.routes }

func (p *ProviderRuntime) setStatus(provider, status string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	value := BotStatus{Provider: provider, Status: status}
	if err != nil {
		value.LastErr = "provider connection unavailable"
	}
	p.statuses[provider] = value
}

func (p *ProviderRuntime) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		cancel()
		return
	}
	p.started = true
	p.cancel = cancel
	telegramEnabled := p.config.TelegramToken != "" && p.processor != nil
	wecomEnabled := p.config.WeComBotID != "" && p.config.WeComSecret != "" && p.processor != nil
	if !telegramEnabled {
		p.statuses[ChannelTelegram] = BotStatus{Provider: ChannelTelegram, Status: ProviderUnconfigured}
	}
	if !wecomEnabled {
		p.statuses[ChannelEnterpriseWeChat] = BotStatus{Provider: ChannelEnterpriseWeChat, Status: ProviderUnconfigured}
	}
	if telegramEnabled {
		p.wg.Add(1)
	}
	if wecomEnabled {
		p.wg.Add(1)
	}
	p.mu.Unlock()
	if telegramEnabled {
		go func() { defer p.wg.Done(); p.telegramLoop(ctx) }()
	}
	if wecomEnabled {
		go func() { defer p.wg.Done(); p.wecomLoop(ctx) }()
	}
}

func (p *ProviderRuntime) Close() {
	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return
	}
	p.started = false
	for provider := range p.statuses {
		p.statuses[provider] = BotStatus{Provider: provider, Status: ProviderStopping}
	}
	cancel := p.cancel
	p.cancel = nil
	if p.wecomConn != nil {
		_ = p.wecomConn.Close()
		p.wecomConn = nil
	}
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	p.wg.Wait()
}

func (p *ProviderRuntime) telegramLoop(ctx context.Context) {
	offset := int64(0)
	for attempt := 0; ctx.Err() == nil; attempt++ {
		p.setStatus(ChannelTelegram, ProviderConnecting, nil)
		if err := p.pollTelegram(ctx, &offset); err != nil && ctx.Err() == nil {
			p.setStatus(ChannelTelegram, ProviderDisconnected, errors.New("telegram polling failed"))
			if !waitProviderBackoff(ctx, attempt) {
				return
			}
		}
	}
}

func (p *ProviderRuntime) pollTelegram(ctx context.Context, offset *int64) error {
	for ctx.Err() == nil {
		endpoint := "https://api.telegram.org/bot" + url.PathEscape(p.config.TelegramToken) + "/getUpdates?timeout=20&allowed_updates=%5B%22message%22%5D"
		if *offset > 0 {
			endpoint += "&offset=" + strconv.FormatInt(*offset, 10)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		response, err := p.client.Do(request)
		if err != nil {
			return err
		}
		var envelope struct {
			OK     bool              `json:"ok"`
			Result []json.RawMessage `json:"result"`
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&envelope)
		response.Body.Close()
		if err != nil || !envelope.OK {
			return errors.New("telegram polling failed")
		}
		p.setStatus(ChannelTelegram, ProviderConnected, nil)
		for _, raw := range envelope.Result {
			var update struct {
				UpdateID int64 `json:"update_id"`
			}
			if err := json.Unmarshal(raw, &update); err != nil {
				continue
			}
			if err := p.processor(ctx, ChannelTelegram, p.config.TelegramUsername, raw); err != nil && !errors.Is(err, ErrProviderMessageIgnored) {
				return errors.New("telegram update processing failed")
			}
			if update.UpdateID >= *offset {
				*offset = update.UpdateID + 1
			}
		}
	}
	return ctx.Err()
}

func (p *ProviderRuntime) wecomLoop(ctx context.Context) {
	for attempt := 0; ctx.Err() == nil; attempt++ {
		p.setStatus(ChannelEnterpriseWeChat, ProviderConnecting, nil)
		if err := p.connectWeCom(ctx); err != nil && ctx.Err() == nil {
			p.setStatus(ChannelEnterpriseWeChat, ProviderDisconnected, errors.New("wecom connection failed"))
			if !waitProviderBackoff(ctx, attempt) {
				return
			}
		}
	}
}

func (p *ProviderRuntime) connectWeCom(ctx context.Context) error {
	config, err := websocket.NewConfig("wss://openws.work.weixin.qq.com", "https://open.work.weixin.qq.com")
	if err != nil {
		return err
	}
	config.Header.Set("Authorization", p.config.WeComSecret)
	connection, err := websocket.DialConfig(config)
	if err != nil {
		return err
	}
	defer connection.Close()
	p.mu.Lock()
	p.wecomConn = connection
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if p.wecomConn == connection {
			p.wecomConn = nil
		}
		p.mu.Unlock()
	}()
	p.setStatus(ChannelEnterpriseWeChat, ProviderConnected, nil)
	for ctx.Err() == nil {
		var frame json.RawMessage
		if err := websocket.JSON.Receive(connection, &frame); err != nil {
			return err
		}
		if err := p.processor(ctx, ChannelEnterpriseWeChat, p.config.WeComBotID, frame); err != nil && !errors.Is(err, ErrProviderMessageIgnored) {
			return errors.New("wecom frame processing failed")
		}
	}
	return ctx.Err()
}

func waitProviderBackoff(ctx context.Context, attempt int) bool {
	if attempt > 5 {
		attempt = 5
	}
	timer := time.NewTimer(time.Duration(1<<attempt) * 100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (p *ProviderRuntime) TelegramTokenConfigured() bool { return p.config.TelegramToken != "" }
func (p *ProviderRuntime) WeComConfigured() bool {
	return p.config.WeComBotID != "" && p.config.WeComSecret != ""
}
