package messagewriter

import (
	"GopherAI/dao/message"
	"GopherAI/model"
	"log"
	"sync"
	"time"
)

// 全局单例 writer，懒加载并自动启动。
var (
	globalWriter *Writer
	writerOnce   sync.Once
)

// GetWriter 返回全局批量写入器（首次调用时创建并启动后台协程）。
func GetWriter() *Writer {
	writerOnce.Do(func() {
		globalWriter = New(Config{})
		globalWriter.Start()
	})
	return globalWriter
}

// Writer 消息批量写入器：攒一批消息后批量落库，避免每条消息单独打一次 DB。
//
// 设计目标：
//  1. 真正异步 —— 只往带缓冲的 channel 投递，不阻塞调用方，也不碰 MQ 网络 IO。
//  2. 批量化 —— 将 N 次 INSERT 合并为 1 次，降低数据库连接往返与压力。
//
// 代价（需明确认知）：进程异常退出时，channel / buffer 中尚未 flush 的消息会丢失。
// 因此本方案只适用于「聊天记录」这类可容忍少量丢失的场景，不适用于订单 / 资金等强一致数据。
type Writer struct {
	ch       chan *model.Message
	buffer   []*model.Message
	flushN   int
	interval time.Duration

	done chan struct{}
	once sync.Once
}

// Config 写入器配置
type Config struct {
	QueueSize int           // channel 缓冲大小，默认 1024
	FlushN    int           // 攒满多少条刷一次，默认 50
	Interval  time.Duration // 定时刷盘间隔，默认 500ms
}

// New 创建批量写入器，调用方需再调用 Start 启动后台协程。
func New(cfg Config) *Writer {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 1024
	}
	if cfg.FlushN <= 0 {
		cfg.FlushN = 50
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 500 * time.Millisecond
	}
	return &Writer{
		ch:       make(chan *model.Message, cfg.QueueSize),
		buffer:   make([]*model.Message, 0, cfg.FlushN),
		flushN:   cfg.FlushN,
		interval: cfg.Interval,
		done:     make(chan struct{}),
	}
}

// Start 启动后台写入协程。
func (w *Writer) Start() {
	go w.run()
}

// Enqueue 投递一条消息。channel 满时会阻塞，为调用方提供天然的背压。
// 写入器停止后返回 false，调用方应停止投递。
func (w *Writer) Enqueue(msg *model.Message) bool {
	select {
	case w.ch <- msg:
		return true
	case <-w.done:
		return false
	}
}

// Stop 停止写入器。run 协程会先把 channel 中已投递的消息消费并 flush 后退出。
// 调用后不应再 Enqueue。
func (w *Writer) Stop() {
	w.once.Do(func() {
		close(w.done)
	})
}

// run 是唯一读写 buffer 的协程，flush 只在这里被调用，因此无需额外加锁。
func (w *Writer) run() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case msg := <-w.ch:
			w.buffer = append(w.buffer, msg)
			if len(w.buffer) >= w.flushN {
				w.flush()
			}
		case <-ticker.C:
			w.flush()
		case <-w.done:
			// 退出前：先 的消息消费掉 channel 里已排队但未进 buffer，再统一 flush。
			w.drain()
			w.flush()
			return
		}
	}
}

// drain 非阻塞地取出 channel 中当前所有消息并放入 buffer。
func (w *Writer) drain() {
	for {
		select {
		case msg := <-w.ch:
			w.buffer = append(w.buffer, msg)
		default:
			return
		}
	}
}

// flush 批量落库，仅在 run 协程内调用。
func (w *Writer) flush() {
	if len(w.buffer) == 0 {
		return
	}

	batch := w.buffer
	w.buffer = make([]*model.Message, 0, w.flushN)

	if err := message.BatchCreate(batch); err != nil {
		log.Printf("[messagewriter] batch create failed, drop %d messages: %v", len(batch), err)
		return
	}
	log.Printf("[messagewriter] batch create success, %d messages", len(batch))
}
