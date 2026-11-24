package devproto

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTopics(t *testing.T) {
	if got := CmdTopic(DeviceLight); got != "home/relay_light/cmd" {
		t.Fatalf("CmdTopic = %q", got)
	}
	if got := StatusTopic(NodeESP32); got != "home/esp32_01/status" {
		t.Fatalf("StatusTopic = %q", got)
	}
	if got := DataTopic(DeviceDHT22); got != "home/dht22_01/data" {
		t.Fatalf("DataTopic = %q", got)
	}
}

func TestParseDeviceID(t *testing.T) {
	cases := []struct {
		topic    string
		id, kind string
		ok       bool
	}{
		{"home/relay_light/status", "relay_light", "status", true},
		{"home/esp32_01/status", "esp32_01", "status", true},
		{"home/relay_fan/cmd", "relay_fan", "cmd", true},
		{"other/relay_fan/cmd", "", "", false},
		{"home/relay_fan", "", "", false},
		{"home//status", "", "", false},
		{"home/a/b/c", "", "", false},
	}
	for _, c := range cases {
		id, kind, ok := ParseDeviceID(c.topic)
		if id != c.id || kind != c.kind || ok != c.ok {
			t.Errorf("ParseDeviceID(%q) = (%q,%q,%v), want (%q,%q,%v)", c.topic, id, kind, ok, c.id, c.kind, c.ok)
		}
	}
}

func TestCommandJSON(t *testing.T) {
	b, err := json.Marshal(Command{
		DeviceID: DeviceCurtain,
		Action:   ActionSetAngle,
		Params:   map[string]any{ParamAngle: 90},
		ReqID:    "r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"device_id":"servo_curtain","action":"set_angle","params":{"angle":90},"req_id":"r1"}`
	if string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}
}

func TestActuatorStatusJSON(t *testing.T) {
	b, err := json.Marshal(ActuatorStatus{DeviceID: DeviceLight, ReqID: "r2", Success: true, State: map[string]any{"power": "on"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{`"device_id":"relay_light"`, `"req_id":"r2"`, `"success":true`, `"state":{"power":"on"}`} {
		if !strings.Contains(string(b), f) {
			t.Errorf("missing %s in %s", f, b)
		}
	}
}
