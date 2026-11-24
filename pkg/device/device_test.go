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

// #5 回执必须来自目标设备：其他设备的 status 即使带回相同 req_id 也不能满足当前请求
func TestCommander_StatusFromWrongDeviceDoesNotSatisfyRequest(t *testing.T) {
	var c *Commander
	pub := &fakePublisher{}
	// 灯的指令，但回执发布到风扇的 status topic，且 req_id 相同
	pub.reply = func(cmd devproto.Command) {
		c.HandleStatus(devproto.StatusTopic(devproto.DeviceFan), statusPayload(devproto.ActuatorStatus{
			DeviceID: devproto.DeviceFan, ReqID: cmd.ReqID, Success: true,
			State: map[string]any{"power": "on"},
		}))
	}
	c = newTestCommander(pub, io.Discard, 150*time.Millisecond)

	start := time.Now()
	_, err := c.Execute(context.Background(), Request{DeviceID: devproto.DeviceLight, Action: devproto.ActionOn})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("wrong-device status must not satisfy request, want ErrTimeout, got %v", err)
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatalf("request should wait for the real device, returned early after %v", time.Since(start))
	}
}

// #5 回执 payload 声称的 device_id 与 topic 不一致时也应丢弃
func TestCommander_StatusPayloadDeviceMismatchIgnored(t *testing.T) {
	var c *Commander
	pub := &fakePublisher{}
	pub.reply = func(cmd devproto.Command) {
		// topic 是灯的，payload 却声称是风扇的回执
		c.HandleStatus(devproto.StatusTopic(cmd.DeviceID), statusPayload(devproto.ActuatorStatus{
			DeviceID: devproto.DeviceFan, ReqID: cmd.ReqID, Success: true,
			State: map[string]any{"power": "on"},
		}))
	}
	c = newTestCommander(pub, io.Discard, 150*time.Millisecond)

	_, err := c.Execute(context.Background(), Request{DeviceID: devproto.DeviceLight, Action: devproto.ActionOn})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("payload device mismatch must be ignored, want ErrTimeout, got %v", err)
	}
}

// #6 合法 JSON 但缺 online 字段时，不能把节点误判为离线
func TestCommander_NodeStatusWithoutOnlineFieldIgnored(t *testing.T) {
	var c *Commander
	pub := &fakePublisher{}
	pub.reply = echoReply(&c, 0)
	c = newTestCommander(pub, io.Discard, time.Second)
	nodeTopic := devproto.StatusTopic(devproto.NodeESP32)

	c.HandleStatus(nodeTopic, []byte(`{"online":true,"conn":"c1"}`))
	c.HandleStatus(nodeTopic, []byte(`{}`))
	c.HandleStatus(nodeTopic, []byte(`{"devices":["relay_light"],"ts":123}`))
	if got := c.NodeState(devproto.NodeESP32); got != NodeOnline {
		t.Fatalf("status without online field must not flip node offline, got %s", got)
	}

	// 正常的离线消息仍然生效
	c.HandleStatus(nodeTopic, []byte(`{"online":false,"reason":"unexpected_disconnect","conn":"c1"}`))
	if got := c.NodeState(devproto.NodeESP32); got != NodeOffline {
		t.Fatalf("normal offline message should apply, got %s", got)
	}
}

// 设备状态缓存：模拟器初始状态（无 req_id）与控制回执都应更新缓存，供 get_device_state 查询
func TestCommander_DeviceStateCached(t *testing.T) {
	var c *Commander
	pub := &fakePublisher{}
	pub.reply = echoReply(&c, 0)
	c = newTestCommander(pub, io.Discard, time.Second)

	// 未上报过：无记录
	if _, ok := c.DeviceState(devproto.DeviceLight); ok {
		t.Fatal("expect no state before any status message")
	}

	// 模拟器上线时发布的初始状态（req_id 为空）也应进入缓存
	c.HandleStatus(devproto.StatusTopic(devproto.DeviceLight), statusPayload(devproto.ActuatorStatus{
		DeviceID: devproto.DeviceLight, Success: true, State: map[string]any{"power": "off"}, TS: 100,
	}))
	st, ok := c.DeviceState(devproto.DeviceLight)
	if !ok || st.State["power"] != "off" || st.TS != 100 {
		t.Fatalf("initial status should be cached, got %+v ok=%v", st, ok)
	}

	// 控制回执更新缓存
	if _, err := c.Execute(context.Background(), Request{DeviceID: devproto.DeviceLight, Action: devproto.ActionOn}); err != nil {
		t.Fatal(err)
	}
	st, ok = c.DeviceState(devproto.DeviceLight)
	if !ok || st.State["power"] != "on" {
		t.Fatalf("control ack should update cache, got %+v ok=%v", st, ok)
	}
}

// 错设备/异常回执不得污染状态缓存
func TestCommander_DeviceStateNotPoisonedByMismatchedStatus(t *testing.T) {
	var c *Commander
	pub := &fakePublisher{}
	pub.reply = echoReply(&c, 0)
	c = newTestCommander(pub, io.Discard, time.Second)

	c.HandleStatus(devproto.StatusTopic(devproto.DeviceLight), statusPayload(devproto.ActuatorStatus{
		DeviceID: devproto.DeviceLight, Success: true, State: map[string]any{"power": "off"}, TS: 100,
	}))
	// payload 声称的 device_id 与 topic 不一致：整条消息不可信，不更新缓存
	c.HandleStatus(devproto.StatusTopic(devproto.DeviceLight), statusPayload(devproto.ActuatorStatus{
		DeviceID: devproto.DeviceFan, Success: true, State: map[string]any{"power": "on"}, TS: 200,
	}))
	st, _ := c.DeviceState(devproto.DeviceLight)
	if st.State["power"] != "off" || st.TS != 100 {
		t.Fatalf("mismatched status must not update cache, got %+v", st)
	}

	// 未注册设备不缓存（HandleStatus 外层已过滤，这里验证防御语义）
	if _, ok := c.DeviceState("tv"); ok {
		t.Fatal("unregistered device must have no state")
	}
}

// #6 同 ClientID 重连后，旧连接迟到的遗嘱不得覆盖新连接的在线状态
func TestCommander_StaleWillFromOldConnectionIgnored(t *testing.T) {
	var c *Commander
	pub := &fakePublisher{}
	pub.reply = echoReply(&c, 0)
	c = newTestCommander(pub, io.Discard, time.Second)
	nodeTopic := devproto.StatusTopic(devproto.NodeESP32)

	// 旧连接上线，随后新连接（相同 ClientID）上线
	c.HandleStatus(nodeTopic, []byte(`{"online":true,"conn":"old"}`))
	c.HandleStatus(nodeTopic, []byte(`{"online":true,"conn":"new"}`))
	// 旧连接的遗嘱迟到到达：不得把节点标记为离线
	c.HandleStatus(nodeTopic, []byte(`{"online":false,"reason":"unexpected_disconnect","conn":"old"}`))
	if got := c.NodeState(devproto.NodeESP32); got != NodeOnline {
		t.Fatalf("stale will must not override new connection online, got %s", got)
	}

	// 新连接自己的遗嘱仍然生效
	c.HandleStatus(nodeTopic, []byte(`{"online":false,"reason":"unexpected_disconnect","conn":"new"}`))
	if got := c.NodeState(devproto.NodeESP32); got != NodeOffline {
		t.Fatalf("current connection will should apply, got %s", got)
	}
}
