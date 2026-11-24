// Package mqtt 封装 paho MQTT 客户端：连接、自动重连后重新订阅、带 context 的发布。
package mqtt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// 默认参数
const (
	DefaultConnectTimeout = 10 * time.Second
	DefaultKeepAlive      = 30 * time.Second
	disconnectQuiesce     = 250 // ms
)

// ErrNotConnected 客户端当前未连接 Broker
var ErrNotConnected = errors.New("mqtt not connected")

// Handler 消息回调
type Handler func(topic string, payload []byte)

// Config 连接参数
type Config struct {
	Broker         string
	ClientID       string
	Username       string
	Password       string
	ConnectTimeout time.Duration
	KeepAlive      time.Duration
}

type subscription struct {
	qos     byte
	handler Handler
}

// Client paho 客户端封装
type Client struct {
	cfg    Config
	client paho.Client
	logger *slog.Logger

	mu   sync.Mutex
	subs map[string]subscription
}

// New 创建客户端（未连接）
func New(cfg Config, logger *slog.Logger) *Client {
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = DefaultConnectTimeout
	}
	if cfg.KeepAlive <= 0 {
		cfg.KeepAlive = DefaultKeepAlive
	}
	if logger == nil {
		logger = slog.Default()
	}
	c := &Client{cfg: cfg, logger: logger, subs: map[string]subscription{}}

	opts := paho.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID).
		SetUsername(cfg.Username).
		SetPassword(cfg.Password).
		SetKeepAlive(cfg.KeepAlive).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetOnConnectHandler(c.onConnect).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			c.logger.Warn("mqtt connection lost", "err", err)
		})
	c.client = paho.NewClient(opts)
	return c
}

// Connect 首次连接，超时或失败返回错误
func (c *Client) Connect(ctx context.Context) error {
	token := c.client.Connect()
	if err := waitToken(ctx, token, c.cfg.ConnectTimeout); err != nil {
		return fmt.Errorf("connect %s: %w", c.cfg.Broker, err)
	}
	return nil
}

// Subscribe 订阅 Topic；记录下来以便断线重连后自动重新订阅
func (c *Client) Subscribe(ctx context.Context, topic string, qos byte, h Handler) error {
	c.mu.Lock()
	c.subs[topic] = subscription{qos: qos, handler: h}
	c.mu.Unlock()
	if !c.client.IsConnectionOpen() {
		return nil
	}
	return c.subscribe(ctx, topic, subscription{qos: qos, handler: h})
}

// Publish 发布消息；未连接时返回 ErrNotConnected
func (c *Client) Publish(ctx context.Context, topic string, qos byte, retained bool, payload []byte) error {
	if !c.client.IsConnectionOpen() {
		return ErrNotConnected
	}
	token := c.client.Publish(topic, qos, retained, payload)
	if err := waitToken(ctx, token, c.cfg.ConnectTimeout); err != nil {
		return fmt.Errorf("publish %s: %w", topic, err)
	}
	return nil
}

// IsConnected 当前是否已连接
func (c *Client) IsConnected() bool { return c.client.IsConnectionOpen() }

// Close 断开连接
func (c *Client) Close() { c.client.Disconnect(disconnectQuiesce) }

func (c *Client) onConnect(_ paho.Client) {
	c.logger.Info("mqtt connected", "broker", c.cfg.Broker, "client_id", c.cfg.ClientID)
	c.mu.Lock()
	subs := make(map[string]subscription, len(c.subs))
	for t, s := range c.subs {
		subs[t] = s
	}
	c.mu.Unlock()

	// 回调运行在 paho 内部 goroutine，不能阻塞等待 SUBACK 太久
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.ConnectTimeout)
	defer cancel()
	for t, s := range subs {
		if err := c.subscribe(ctx, t, s); err != nil {
			c.logger.Error("mqtt resubscribe failed", "topic", t, "err", err)
		}
	}
}

func (c *Client) subscribe(ctx context.Context, topic string, s subscription) error {
	token := c.client.Subscribe(topic, s.qos, func(_ paho.Client, m paho.Message) {
		s.handler(m.Topic(), m.Payload())
	})
	if err := waitToken(ctx, token, c.cfg.ConnectTimeout); err != nil {
		return fmt.Errorf("subscribe %s: %w", topic, err)
	}
	c.logger.Info("mqtt subscribed", "topic", topic)
	return nil
}

func waitToken(ctx context.Context, t paho.Token, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-t.Done():
		return t.Error()
	case <-timer.C:
		return fmt.Errorf("timeout after %s", timeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}
