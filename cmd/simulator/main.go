// Command simulator 虚拟 ESP32 设备节点，在无硬件阶段替代真实设备联调。
//
// 协议与 design.md 第七章一致：
//   - 上报 home/{dht22_01,bh1750_01}/data
//   - 订阅 home/{relay_light,relay_fan,servo_curtain}/cmd
//   - 回报 home/{device_id}/status（Retain），节点在线状态带 Last Will
//
// 用法：
//
//	go run ./cmd/simulator                        # 正常运行，Ctrl+C 优雅下线
//	go run ./cmd/simulator -crash-after 30s       # 30 秒后模拟断电，验证 Last Will
//	go run ./cmd/simulator -anomaly-rate 0.05     # 5% 概率产生 85℃ 异常读数
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	defaultBroker    = "tcp://127.0.0.1:1883"
	defaultNodeID    = "esp32_01"
	defaultInterval  = 2 * time.Second
	defaultKeepAlive = 5 * time.Second
	crashExitCode    = 2
)

func main() {
	cfg := simulatorConfig{}
	flag.StringVar(&cfg.Broker, "broker", envOr("MQTT_BROKER", defaultBroker), "MQTT broker 地址")
	flag.StringVar(&cfg.NodeID, "node", defaultNodeID, "节点 ID（MQTT clientId，同时用于节点状态 Topic）")
	flag.StringVar(&cfg.Username, "username", os.Getenv("MQTT_USERNAME"), "MQTT 用户名")
	flag.StringVar(&cfg.Password, "password", os.Getenv("MQTT_PASSWORD"), "MQTT 密码")
	flag.DurationVar(&cfg.Interval, "interval", defaultInterval, "传感器上报周期")
	flag.DurationVar(&cfg.KeepAlive, "keepalive", defaultKeepAlive, "MQTT KeepAlive，决定 Broker 判定掉线的时延（约 1.5 倍）")
	anomalyRate := flag.Float64("anomaly-rate", 0, "温度异常读数（85℃）概率，0~1")
	crashAfter := flag.Duration("crash-after", 0, "运行指定时长后模拟断电（不发 DISCONNECT），0 表示不模拟")
	statusDelay := flag.Duration("status-delay", 0, "收到指令后延迟多久才回执（例如 5s），用于验证超时与迟到回执")
	dropStatus := flag.Bool("drop-status", false, "收到指令后完全不回执（节点保持在线），用于验证回执超时")
	verbose := flag.Bool("v", false, "输出每条传感器上报日志")
	flag.Parse()

	if *statusDelay < 0 {
		slog.Error("status-delay must be >= 0")
		os.Exit(2)
	}
	cfg.StatusDelay = *statusDelay
	cfg.DropStatus = *dropStatus

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
	//连接
	sim := newSimulator(cfg, newEnvironment(time.Now().UnixNano(), *anomalyRate))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *crashAfter > 0 {
		time.AfterFunc(*crashAfter, func() {
			slog.Warn("simulating power loss, broker should publish last will", "after", *crashAfter)
			os.Exit(crashExitCode)
		})
	}

	if err := sim.Run(ctx); err != nil {
		slog.Error("simulator exited", "err", err)
		os.Exit(1)
	}
	sim.Shutdown()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
