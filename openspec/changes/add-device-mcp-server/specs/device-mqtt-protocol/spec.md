## Purpose

定义设备下行控制指令与执行回执的 MQTT 协议约定，使 MCP Server、模拟器与 ESP32 固件对 Topic、消息字段和请求关联方式有统一的契约。

## ADDED Requirements

### Requirement: 下行指令 Topic 与投递语义
控制指令 SHALL 发布到 `home/{device_id}/cmd`，QoS 为 1，且 MUST NOT 设置 Retain，以避免设备重连时重复执行历史指令。

#### Scenario: 指令不被保留
- **WHEN** 一条指令发布到 `home/relay_light/cmd` 后，设备断线再重连并重新订阅
- **THEN** 设备不会收到该历史指令

### Requirement: 指令消息携带请求标识
指令消息 SHALL 为 JSON，包含 `device_id`、`action`、`params`、`req_id` 四个字段。`req_id` 由发送方生成，在发送方进程内唯一；`params` 无参数时为空对象。

#### Scenario: 继电器开指令
- **WHEN** 发送方要打开灯
- **THEN** 发布 `{"device_id":"relay_light","action":"on","params":{},"req_id":"<唯一值>"}`

#### Scenario: 舵机角度指令
- **WHEN** 发送方要把窗帘设为 90°
- **THEN** 发布 `{"device_id":"servo_curtain","action":"set_angle","params":{"angle":90},"req_id":"<唯一值>"}`

### Requirement: 执行回执原样带回请求标识
设备执行指令后 SHALL 发布回执到 `home/{device_id}/status`（QoS 1，Retain），回执包含 `device_id`、`action`、`req_id`（与指令一致）、`success`、`state`（执行后的设备状态），失败时包含 `error`。设备上线时主动发布的状态回执 `req_id` 为空。

#### Scenario: 执行成功回执
- **WHEN** 设备收到 `req_id` 为 `a1b2` 的开灯指令并执行成功
- **THEN** 发布 `{"device_id":"relay_light","action":"on","req_id":"a1b2","success":true,"state":{"power":"on"},...}`

#### Scenario: 执行失败回执
- **WHEN** 设备收到参数非法的指令（如窗帘角度 200）
- **THEN** 发布 `success:false` 且 `error` 描述原因的回执，`req_id` 与指令一致，`state` 为未改变的当前状态

#### Scenario: 兼容缺少请求标识的指令
- **WHEN** 设备收到不含 `req_id` 的指令
- **THEN** 正常执行，回执中 `req_id` 为空

### Requirement: 设备标识以 Topic 为准
设备 SHALL 以指令 Topic 中的 `{device_id}` 作为目标设备，忽略 payload 中不一致的 `device_id`。

#### Scenario: payload 与 Topic 不一致
- **WHEN** 向 `home/relay_fan/cmd` 发布 payload 中 `device_id` 为 `relay_light` 的指令
- **THEN** 设备操作风扇，回执发布到 `home/relay_fan/status`
