package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"GopherAI/pkg/devproto"
)

func ptr(f float64) *float64 { return &f }

func TestRegistry_Validate(t *testing.T) {
	reg := NewRegistry(DefaultDevices())
	cases := []struct {
		name    string
		req     Request
		wantErr bool
		wantSub string
	}{
		{"灯开", Request{DeviceID: devproto.DeviceLight, Action: devproto.ActionOn}, false, ""},
		{"灯忽略 angle", Request{DeviceID: devproto.DeviceLight, Action: devproto.ActionOff, Angle: ptr(30)}, false, ""},
		{"窗帘合法", Request{DeviceID: devproto.DeviceCurtain, Action: devproto.ActionSetAngle, Angle: ptr(90)}, false, ""},
		{"灯不支持 set_angle", Request{DeviceID: devproto.DeviceLight, Action: devproto.ActionSetAngle, Angle: ptr(90)}, true, "只支持"},
		{"角度越界", Request{DeviceID: devproto.DeviceCurtain, Action: devproto.ActionSetAngle, Angle: ptr(200)}, true, "超出范围"},
		{"负角度", Request{DeviceID: devproto.DeviceCurtain, Action: devproto.ActionSetAngle, Angle: ptr(-1)}, true, "超出范围"},
		{"小数角度", Request{DeviceID: devproto.DeviceCurtain, Action: devproto.ActionSetAngle, Angle: ptr(45.5)}, true, "整数"},
		{"缺少角度", Request{DeviceID: devproto.DeviceCurtain, Action: devproto.ActionSetAngle}, true, "需要 angle"},
		{"未知设备", Request{DeviceID: "tv", Action: devproto.ActionOn}, true, "relay_light"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, cmd, err := reg.Validate(c.req)
			if (err != nil) != c.wantErr {
				t.Fatalf("wantErr=%v got %v", c.wantErr, err)
			}
			if err != nil {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("want ErrValidation, got %v", err)
				}
				if !strings.Contains(err.Error(), c.wantSub) {
					t.Fatalf("error %q should contain %q", err, c.wantSub)
				}
				return
			}
			if c.req.Action == devproto.ActionSetAngle && cmd.Params[devproto.ParamAngle] != 90 {
				t.Fatalf("params.angle = %v", cmd.Params[devproto.ParamAngle])
			}
			if c.req.Action != devproto.ActionSetAngle && len(cmd.Params) != 0 {
				t.Fatalf("relay params should be empty, got %v", cmd.Params)
			}
		})
	}
}

func TestError_As(t *testing.T) {
	var err error = newErr(ErrTimeout, "超时了")
	var be *Error
	if !errors.As(err, &be) || be.Reason != "超时了" || !errors.Is(err, ErrTimeout) {
		t.Fatalf("errors.As/Is failed: %v", err)
	}
}

// fakePublisher 记录发布并按 reply 回调模拟设备
type fakePublisher struct {
	mu        sync.Mutex
	published []devproto.Command
	reply     func(cmd devproto.Command)
	err       error
}

func (f *fakePublisher) Publish(_ context.Context, topic string, qos byte, retained bool, payload []byte) error {
	if f.err != nil {
		return f.err
	}
	if retained || qos != devproto.QoSCmd {
		return fmt.Errorf("cmd must be qos1 non-retained")
	}
	var cmd devproto.Command
	if err := json.Unmarshal(payload, &cmd); err != nil {
		return err
	}
	if topic != devproto.CmdTopic(cmd.DeviceID) {
		return fmt.Errorf("topic mismatch %s", topic)
	}
	f.mu.Lock()
	f.published = append(f.published, cmd)
	f.mu.Unlock()
	if f.reply != nil {
		go f.reply(cmd)
	}
	return nil
}

func (f *fakePublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.published)
}

func newTestCommander(pub *fakePublisher, logBuf io.Writer, timeout time.Duration) *Commander {
	var n atomic.Int64
	logger := slog.New(slog.NewTextHandler(logBuf, nil))
	return NewCommander(NewRegistry(DefaultDevices()), pub,
		WithTimeout(timeout),
		WithLogger(logger),
		WithIDGenerator(func() string { return fmt.Sprintf("req-%d", n.Add(1)) }),
	)
}

func statusPayload(st devproto.ActuatorStatus) []byte {
	b, _ := json.Marshal(st)
	return b
}

// echoReply 模拟设备：延迟后回执成功，状态反映指令
func echoReply(c **Commander, delay time.Duration) func(devproto.Command) {
	return func(cmd devproto.Command) {
		time.Sleep(delay)
		state := map[string]any{"power": cmd.Action}
		if cmd.Action == devproto.ActionSetAngle {
			state = map[string]any{"angle": cmd.Params[devproto.ParamAngle]}
		}
		(*c).HandleStatus(devproto.StatusTopic(cmd.DeviceID), statusPayload(devproto.ActuatorStatus{
			DeviceID: cmd.DeviceID, Action: cmd.Action, ReqID: cmd.ReqID, Success: true, State: state,
		}))
	}
}

func TestCommander_Success(t *testing.T) {
	var buf bytes.Buffer
	var c *Commander
	pub := &fakePublisher{}
	pub.reply = echoReply(&c, 20*time.Millisecond)
	c = newTestCommander(pub, &buf, time.Second)

	out, err := c.Execute(context.Background(), Request{DeviceID: devproto.DeviceFan, Action: devproto.ActionOn})
	if err != nil {
		t.Fatal(err)
	}
	if out.State["power"] != "on" || out.Latency <= 0 {
		t.Fatalf("unexpected outcome %+v", out)
	}
	log := buf.String()
	for _, want := range []string{"msg=device_control", "req_id=req-1", "result=success", "latency_ms="} {
		if !strings.Contains(log, want) {
			t.Fatalf("log missing %q: %s", want, log)
		}
	}
}

func TestCommander_DeviceFailed(t *testing.T) {
	var c *Commander
	pub := &fakePublisher{}
	pub.reply = func(cmd devproto.Command) {
		c.HandleStatus(devproto.StatusTopic(cmd.DeviceID), statusPayload(devproto.ActuatorStatus{
			DeviceID: cmd.DeviceID, ReqID: cmd.ReqID, Success: false, Error: "servo jammed",
		}))
	}
	c = newTestCommander(pub, io.Discard, time.Second)
	_, err := c.Execute(context.Background(), Request{DeviceID: devproto.DeviceCurtain, Action: devproto.ActionSetAngle, Angle: ptr(10)})
	if !errors.Is(err, ErrDeviceFailed) || !strings.Contains(err.Error(), "servo jammed") {
		t.Fatalf("want device failed, got %v", err)
	}
}

func TestCommander_TimeoutIgnoresStaleAndEmptyReqID(t *testing.T) {
	var buf bytes.Buffer
	var c *Commander
	pub := &fakePublisher{}
	pub.reply = func(cmd devproto.Command) {
		topic := devproto.StatusTopic(cmd.DeviceID)
		c.HandleStatus(topic, statusPayload(devproto.ActuatorStatus{DeviceID: cmd.DeviceID, Success: true}))
		c.HandleStatus(topic, statusPayload(devproto.ActuatorStatus{DeviceID: cmd.DeviceID, ReqID: "old", Success: true}))
	}
	c = newTestCommander(pub, &buf, 100*time.Millisecond)
	_, err := c.Execute(context.Background(), Request{DeviceID: devproto.DeviceLight, Action: devproto.ActionOn})
	if !errors.Is(err, ErrTimeout) || !strings.Contains(err.Error(), "未知") {
		t.Fatalf("want timeout, got %v", err)
	}
	if !strings.Contains(buf.String(), "result=timeout") {
		t.Fatalf("log missing timeout: %s", buf.String())
	}
}

func TestCommander_ConcurrentNoCrossTalk(t *testing.T) {
	var c *Commander
	pub := &fakePublisher{}
	pub.reply = echoReply(&c, 30*time.Millisecond)
	c = newTestCommander(pub, io.Discard, time.Second)

	var wg sync.WaitGroup
	var lightOut, curtainOut Outcome
	var lightErr, curtainErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		lightOut, lightErr = c.Execute(context.Background(), Request{DeviceID: devproto.DeviceLight, Action: devproto.ActionOn})
	}()
	go func() {
		defer wg.Done()
		curtainOut, curtainErr = c.Execute(context.Background(), Request{DeviceID: devproto.DeviceCurtain, Action: devproto.ActionSetAngle, Angle: ptr(90)})
	}()
	wg.Wait()
	if lightErr != nil || curtainErr != nil {
		t.Fatalf("errors: %v %v", lightErr, curtainErr)
	}
	if lightOut.Device.ID != devproto.DeviceLight || lightOut.State["power"] != "on" {
		t.Fatalf("light outcome wrong %+v", lightOut)
	}
	if curtainOut.Device.ID != devproto.DeviceCurtain || curtainOut.State["angle"] != float64(90) {
		t.Fatalf("curtain outcome wrong %+v", curtainOut)
	}
}

func TestCommander_OfflineFastFailAndRecover(t *testing.T) {
	var buf bytes.Buffer
	var c *Commander
	pub := &fakePublisher{}
	pub.reply = echoReply(&c, 0)
	c = newTestCommander(pub, &buf, time.Second)
	nodeTopic := devproto.StatusTopic(devproto.NodeESP32)

	c.HandleStatus(nodeTopic, []byte(`{"online":false,"reason":"unexpected_disconnect"}`))
	start := time.Now()
	_, err := c.Execute(context.Background(), Request{DeviceID: devproto.DeviceLight, Action: devproto.ActionOn})
	if !errors.Is(err, ErrOffline) {
		t.Fatalf("want offline, got %v", err)
	}
	if time.Since(start) > 50*time.Millisecond || pub.count() != 0 {
		t.Fatalf("offline should fail fast without publishing")
	}
	if !strings.Contains(buf.String(), "result=offline") || !strings.Contains(buf.String(), "latency_ms=0") {
		t.Fatalf("log wrong: %s", buf.String())
	}

	c.HandleStatus(nodeTopic, []byte(`{"online":true}`))
	if _, err := c.Execute(context.Background(), Request{DeviceID: devproto.DeviceLight, Action: devproto.ActionOn}); err != nil {
		t.Fatalf("should recover after online: %v", err)
	}
}

func TestCommander_ValidationNoPublish(t *testing.T) {
	var buf bytes.Buffer
	pub := &fakePublisher{}
	c := newTestCommander(pub, &buf, time.Second)
	_, err := c.Execute(context.Background(), Request{DeviceID: devproto.DeviceCurtain, Action: devproto.ActionSetAngle, Angle: ptr(200)})
	if !errors.Is(err, ErrValidation) || pub.count() != 0 {
		t.Fatalf("want validation without publish, got %v count=%d", err, pub.count())
	}
	if !strings.Contains(buf.String(), "result=validation_error") || !strings.Contains(buf.String(), "latency_ms=0") {
		t.Fatalf("log wrong: %s", buf.String())
	}
}

func TestCommander_NotConnected(t *testing.T) {
	pub := &fakePublisher{err: ErrNotConnected}
	c := newTestCommander(pub, io.Discard, time.Second)
	_, err := c.Execute(context.Background(), Request{DeviceID: devproto.DeviceFan, Action: devproto.ActionOff})
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("want not connected, got %v", err)
	}
}

func TestCommander_ContextCanceled(t *testing.T) {
	pub := &fakePublisher{}
	c := newTestCommander(pub, io.Discard, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.Execute(ctx, Request{DeviceID: devproto.DeviceFan, Action: devproto.ActionOn})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want ctx error, got %v", err)
	}
}
