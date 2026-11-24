package main

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sync"

	"GopherAI/pkg/devproto"
)

// 设备 ID，统一引用协议包
const (
	idDHT22   = devproto.DeviceDHT22
	idBH1750  = devproto.DeviceBH1750
	idLight   = devproto.DeviceLight
	idFan     = devproto.DeviceFan
	idCurtain = devproto.DeviceCurtain
)

type (
	sensorData = devproto.SensorData
	command    = devproto.Command
)

// 物理模型参数
const (
	baseTemp        = 27.0  // 无风扇时的室温基准（℃）
	fanCooling      = 3.0   // 风扇开启后目标温度下降量（℃）
	baseHumidity    = 55.0  // 湿度基准（%RH）
	outdoorLux      = 600.0 // 窗帘全开时进入室内的自然光（lux）
	lampLux         = 300.0 // 灯开启贡献的光照（lux）
	convergeRate    = 0.1   // 每个采样周期向目标值靠拢的比例
	tempNoise       = 0.15
	humidityNoise   = 0.8
	luxNoise        = 15.0
	dht22ErrorValue = 85.0 // DHT22 典型错误读数，用于验证后端异常治理
	maxServoAngle   = devproto.MaxAngle
)

const (
	actionOn       = devproto.ActionOn
	actionOff      = devproto.ActionOff
	actionSetAngle = devproto.ActionSetAngle
)

// environment 虚拟房间：执行器状态会反向影响传感器读数，便于演示“开风扇→降温”
type environment struct {
	mu           sync.Mutex
	rnd          *rand.Rand
	lightOn      bool
	fanOn        bool
	curtainAngle int
	temp         float64
	humidity     float64
	anomalyRate  float64
}

func newEnvironment(seed int64, anomalyRate float64) *environment {
	return &environment{
		rnd:          rand.New(rand.NewSource(seed)),
		curtainAngle: maxServoAngle / 2,
		temp:         baseTemp,
		humidity:     baseHumidity,
		anomalyRate:  anomalyRate,
	}
}

// sample 推进一个采样周期并返回各传感器读数
func (e *environment) sample() []sensorData {
	e.mu.Lock()
	defer e.mu.Unlock()

	targetTemp := baseTemp
	if e.fanOn {
		targetTemp -= fanCooling
	}
	e.temp += (targetTemp-e.temp)*convergeRate + e.rnd.NormFloat64()*tempNoise
	e.humidity += (baseHumidity-e.humidity)*convergeRate + e.rnd.NormFloat64()*humidityNoise

	lux := outdoorLux * float64(e.curtainAngle) / maxServoAngle
	if e.lightOn {
		lux += lampLux
	}
	lux = math.Max(0, lux+e.rnd.NormFloat64()*luxNoise)

	temp := e.temp
	if e.rnd.Float64() < e.anomalyRate {
		temp = dht22ErrorValue
	}

	return []sensorData{
		{DeviceID: idDHT22, Type: "temperature", Value: round1(temp)},
		{DeviceID: idDHT22, Type: "humidity", Value: round1(e.humidity)},
		{DeviceID: idBH1750, Type: "light", Value: round1(lux)},
	}
}

// apply 执行控制指令，返回执行后的设备状态
func (e *environment) apply(cmd command) (map[string]any, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch cmd.DeviceID {
	case idLight:
		on, err := relayAction(cmd.Action)
		if err != nil {
			return e.stateLocked(cmd.DeviceID), err
		}
		e.lightOn = on
	case idFan:
		on, err := relayAction(cmd.Action)
		if err != nil {
			return e.stateLocked(cmd.DeviceID), err
		}
		e.fanOn = on
	case idCurtain:
		angle, err := servoAngle(cmd)
		if err != nil {
			return e.stateLocked(cmd.DeviceID), err
		}
		e.curtainAngle = angle
	default:
		return nil, fmt.Errorf("unknown device %q", cmd.DeviceID)
	}
	return e.stateLocked(cmd.DeviceID), nil
}

// state 返回执行器当前状态
func (e *environment) state(id string) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stateLocked(id)
}

func (e *environment) stateLocked(id string) map[string]any {
	switch id {
	case idLight:
		return map[string]any{"power": onOff(e.lightOn)}
	case idFan:
		return map[string]any{"power": onOff(e.fanOn)}
	case idCurtain:
		return map[string]any{"angle": e.curtainAngle}
	default:
		return map[string]any{}
	}
}

func relayAction(action string) (bool, error) {
	switch action {
	case actionOn:
		return true, nil
	case actionOff:
		return false, nil
	default:
		return false, fmt.Errorf("relay does not support action %q", action)
	}
}

func servoAngle(cmd command) (int, error) {
	if cmd.Action != actionSetAngle {
		return 0, fmt.Errorf("servo does not support action %q", cmd.Action)
	}
	raw, ok := cmd.Params[devproto.ParamAngle]
	if !ok {
		return 0, errors.New("missing param angle")
	}
	// JSON 数字默认解码为 float64
	v, ok := raw.(float64)
	if !ok {
		return 0, fmt.Errorf("param angle must be number, got %T", raw)
	}
	if v < 0 || v > maxServoAngle {
		return 0, fmt.Errorf("angle %v out of range [0,%d]", v, maxServoAngle)
	}
	return int(v), nil
}

func onOff(b bool) string {
	if b {
		return actionOn
	}
	return actionOff
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// actuatorIDs 节点挂载的执行器
var actuatorIDs = []string{idLight, idFan, idCurtain}

// allDeviceIDs 节点挂载的全部设备
var allDeviceIDs = []string{idDHT22, idBH1750, idLight, idFan, idCurtain}
