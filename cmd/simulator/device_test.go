package main

import "testing"

func TestEnvironment_FanCoolsRoom(t *testing.T) {
	env := newEnvironment(1, 0)
	if _, err := env.apply(command{DeviceID: idFan, Action: actionOn}); err != nil {
		t.Fatalf("apply fan on: %v", err)
	}
	var last float64
	for i := 0; i < 100; i++ {
		last = env.sample()[0].Value
	}
	if last > baseTemp-fanCooling+1 {
		t.Fatalf("temperature should converge near %.1f, got %.1f", baseTemp-fanCooling, last)
	}
}

func TestEnvironment_Apply(t *testing.T) {
	cases := []struct {
		name    string
		cmd     command
		wantErr bool
	}{
		{"light on", command{DeviceID: idLight, Action: actionOn}, false},
		{"relay bad action", command{DeviceID: idFan, Action: actionSetAngle}, true},
		{"curtain ok", command{DeviceID: idCurtain, Action: actionSetAngle, Params: map[string]any{"angle": 90.0}}, false},
		{"curtain out of range", command{DeviceID: idCurtain, Action: actionSetAngle, Params: map[string]any{"angle": 200.0}}, true},
		{"curtain missing angle", command{DeviceID: idCurtain, Action: actionSetAngle}, true},
		{"unknown device", command{DeviceID: "nope", Action: actionOn}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newEnvironment(1, 0)
			_, err := env.apply(c.cmd)
			if (err != nil) != c.wantErr {
				t.Fatalf("wantErr=%v, got %v", c.wantErr, err)
			}
		})
	}
}

func TestEnvironment_AnomalyAlways(t *testing.T) {
	env := newEnvironment(1, 1)
	if v := env.sample()[0].Value; v != dht22ErrorValue {
		t.Fatalf("want anomaly %.1f, got %.1f", dht22ErrorValue, v)
	}
}

func TestParseCommand_TopicWinsAndReqIDEchoed(t *testing.T) {
	cmd, err := parseCommand("home/relay_fan/cmd", []byte(`{"device_id":"relay_light","action":"on","req_id":"abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cmd.DeviceID != idFan {
		t.Fatalf("device_id should follow topic, got %q", cmd.DeviceID)
	}
	env := newEnvironment(1, 0)
	state, applyErr := env.apply(cmd)
	st := buildStatus(cmd.DeviceID, cmd, state, applyErr)
	if st.ReqID != "abc" || !st.Success || st.DeviceID != idFan {
		t.Fatalf("unexpected status %+v", st)
	}
}

func TestBuildStatus_NoReqIDAndFailure(t *testing.T) {
	cmd, err := parseCommand("home/servo_curtain/cmd", []byte(`{"action":"set_angle","params":{"angle":200}}`))
	if err != nil {
		t.Fatal(err)
	}
	env := newEnvironment(1, 0)
	state, applyErr := env.apply(cmd)
	st := buildStatus(cmd.DeviceID, cmd, state, applyErr)
	if st.ReqID != "" || st.Success || st.Error == "" {
		t.Fatalf("unexpected status %+v", st)
	}
}
