// Command device-mcp-server 设备控制 MCP Server（独立进程，Streamable HTTP）。
//
// 用法：
//
//	go run ./cmd/device-mcp-server
//	go run ./cmd/device-mcp-server -http-addr :8081 -broker tcp://127.0.0.1:1883 -cmd-timeout 3s
//
// 端点：http://localhost:8081/mcp
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	mcpserver "GopherAI/common/mcp/server"
	"GopherAI/common/mqtt"
	"GopherAI/pkg/device"
	"GopherAI/pkg/devproto"
)

const (
	defaultHTTPAddr = ":8081"
	defaultBroker   = "tcp://127.0.0.1:1883"
	defaultClientID = "device-mcp-server"
	endpointPath    = "/mcp"
	shutdownTimeout = 5 * time.Second
)

func main() {
	if err := run(); err != nil {
		slog.Error("device-mcp-server exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	httpAddr := flag.String("http-addr", defaultHTTPAddr, "MCP HTTP 监听地址")
	broker := flag.String("broker", envOr("MQTT_BROKER", defaultBroker), "MQTT Broker 地址")
	clientID := flag.String("client-id", defaultClientID, "MQTT clientId 前缀（自动追加 -<PID>）")
	username := flag.String("username", os.Getenv("MQTT_USERNAME"), "MQTT 用户名")
	password := flag.String("password", os.Getenv("MQTT_PASSWORD"), "MQTT 密码")
	timeout := flag.Duration("cmd-timeout", device.DefaultTimeout, "等待设备回执超时")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mq := mqtt.New(mqtt.Config{
		Broker:   *broker,
		ClientID: fmt.Sprintf("%s-%d", *clientID, os.Getpid()),
		Username: *username,
		Password: *password,
	}, logger)

	commander := device.NewCommander(
		device.NewRegistry(device.DefaultDevices()),
		publisher{mq},
		device.WithTimeout(*timeout),
		device.WithLogger(logger),
	)
	// 先登记订阅，连接成功后由 OnConnect 统一订阅（重连时也会重新订阅）
	if err := mq.Subscribe(ctx, devproto.StatusWildcard, devproto.QoSStatus, commander.HandleStatus); err != nil {
		return err
	}
	if err := mq.Connect(ctx); err != nil {
		return err
	}
	defer mq.Close()

	httpServer := server.NewStreamableHTTPServer(mcpserver.New(commander), server.WithEndpointPath(endpointPath))
	errCh := make(chan error, 1)
	go func() {
		logger.Info("mcp server listening", "url", fmt.Sprintf("http://localhost%s%s", *httpAddr, endpointPath))
		errCh <- httpServer.Start(*httpAddr)
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// publisher 适配 common/mqtt 到 device.Publisher，统一错误类别
type publisher struct{ c *mqtt.Client }

func (p publisher) Publish(ctx context.Context, topic string, qos byte, retained bool, payload []byte) error {
	err := p.c.Publish(ctx, topic, qos, retained, payload)
	if errors.Is(err, mqtt.ErrNotConnected) {
		return fmt.Errorf("%w: %v", device.ErrNotConnected, err)
	}
	return err
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
