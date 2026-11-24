<template>
  <div class="console-page">
    <div class="device">
      <div class="device-top">
        <span class="led"></span>
        <span class="plate">智能家居控制台</span>
        <span class="plate-sub">SMART HOME CONSOLE</span>
      </div>

      <div class="device-body">
        <p class="device-hint">ESP32 · 灯 / 风扇 / 窗帘</p>
        <button class="enter-btn" @click="$router.push('/ai-chat')">
          <span class="enter-title">对话控制</span>
          <span class="enter-sub">用一句自然语言控制家中设备</span>
        </button>
      </div>

      <div class="device-foot">GOPHERAI · LLM + MCP</div>
    </div>
  </div>
</template>

<script>
export default {
  name: 'MenuView'
}
</script>

<style scoped>
.console-page {
  min-height: 100vh;
  display: flex;
  justify-content: center;
  align-items: center;
  background: #e7e4dc;
  font-family: "PingFang SC", "Microsoft YaHei", "Helvetica Neue", Arial, sans-serif;
}

/* 挂在墙上的控制面板 */
.device {
  width: 460px;
  max-width: calc(100vw - 48px);
  background: #24272c;
  border-radius: 12px;
  border: 1px solid #101215;
  box-shadow:
    0 1px 0 rgba(255, 255, 255, 0.14) inset,
    0 24px 60px rgba(0, 0, 0, 0.28),
    0 4px 12px rgba(0, 0, 0, 0.18);
  padding: 22px 26px 18px;
}

.device-top {
  display: flex;
  align-items: center;
  gap: 10px;
  padding-bottom: 16px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
}

.led {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: #43d17c;
  box-shadow: 0 0 8px #43d17c;
  animation: ledBreathe 2.4s ease-in-out infinite;
}

@keyframes ledBreathe {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.45; }
}

.plate {
  font-family: Consolas, "Courier New", monospace;
  letter-spacing: 0.14em;
  font-size: 15px;
  font-weight: 700;
  color: #e4e8ed;
}

.plate-sub {
  margin-left: auto;
  font-family: Consolas, "Courier New", monospace;
  letter-spacing: 0.18em;
  font-size: 10px;
  color: #6b727c;
}

.device-body {
  padding: 26px 4px 22px;
  text-align: center;
}

.device-hint {
  margin: 0 0 22px;
  font-family: Consolas, "Courier New", monospace;
  letter-spacing: 0.14em;
  font-size: 11px;
  color: #8b939e;
}

/* 主按键：琥珀色实体按钮 */
.enter-btn {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 22px 0 20px;
  cursor: pointer;
  background: #e8a33d;
  color: #1c1403;
  border: none;
  border-bottom: 4px solid #b97f24;
  border-radius: 8px;
  transition: transform 0.08s ease, border-bottom-width 0.08s ease, background 0.15s ease;
}

.enter-btn:hover {
  background: #f0b456;
}

.enter-btn:active {
  transform: translateY(3px);
  border-bottom-width: 1px;
}

.enter-title {
  font-size: 20px;
  font-weight: 700;
  letter-spacing: 0.08em;
}

.enter-sub {
  font-size: 12.5px;
  font-weight: 500;
  opacity: 0.75;
}

.device-foot {
  padding-top: 14px;
  border-top: 1px solid rgba(255, 255, 255, 0.08);
  text-align: center;
  font-family: Consolas, "Courier New", monospace;
  letter-spacing: 0.2em;
  font-size: 10px;
  color: #5a616b;
}
</style>
