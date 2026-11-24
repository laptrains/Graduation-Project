#!/usr/bin/env bash
# GopherAI-v2 本地启动脚本（Git Bash / Linux / macOS）
#
# 用法：
#   ./start.sh                启动后端（默认，监听 config.toml 中的端口）
#   ./start.sh web            只启动前端（后端需已运行）
#   ./start.sh all            同时启动后端 + 前端
#   ./start.sh all --no-build 跳过重新编译，直接运行已有二进制
#   ./start.sh all --skip-checks   跳过 MySQL/Redis 连通性检查
#
# 环境变量（也可写入项目根目录的 .env 文件，脚本会自动加载）：
#   OPENAI_API_KEY / OPENAI_MODEL_NAME / OPENAI_BASE_URL        对话模型（必填）
#   DEEPSEEK_API_KEY / DEEPSEEK_BASE_URL / DEEPSEEK_MODEL_NAME  摘要模型（可选）
#     未配置 DEEPSEEK_API_KEY 时服务仍可启动，只是不生成会话摘要
#
# 停止：Ctrl + C
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

RED='\033[31m'; GREEN='\033[32m'; YELLOW='\033[33m'; BLUE='\033[34m'; RESET='\033[0m'
info()  { echo -e "${BLUE}[info]${RESET}  $*"; }
ok()    { echo -e "${GREEN}[ok]${RESET}    $*"; }
warn()  { echo -e "${YELLOW}[warn]${RESET}  $*"; }
err()   { echo -e "${RED}[error]${RESET} $*" >&2; }

usage() {
  sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
}

# ---------- 参数 ----------
TARGET="backend"
NO_BUILD=0
NO_INSTALL=0
SKIP_CHECKS=0

for arg in "$@"; do
  case "$arg" in
    backend|server)   TARGET="backend" ;;
    web|frontend)     TARGET="web" ;;
    all)              TARGET="all" ;;
    --no-build)       NO_BUILD=1 ;;
    --no-install)     NO_INSTALL=1 ;;
    --skip-checks)    SKIP_CHECKS=1 ;;
    -h|--help)        usage; exit 0 ;;
    *) err "未知参数: $arg"; usage; exit 1 ;;
  esac
done

# ---------- 读取配置 ----------
# 从 config.toml 指定 section 中读取 key 的值
cfg_get() {
  awk -v sec="$1" -v key="$2" '
    /^[[:space:]]*\[/ {
      name=$0
      gsub(/^[[:space:]]*\[[[:space:]]*|[[:space:]]*\].*$/, "", name)
      insec = (name == sec)
      next
    }
    insec {
      line=$0
      sub(/#.*$/, "", line)
      n = index(line, "=")
      if (n == 0) next
      k = substr(line, 1, n-1); v = substr(line, n+1)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", k)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", v)
      gsub(/^"|"$/, "", v)
      if (k == key) { print v; exit }
    }
  ' config/config.toml
}

PORT="$(cfg_get mainConfig port)"
PORT="${PORT:-9090}"
MYSQL_HOST="$(cfg_get mysqlConfig host)"; MYSQL_PORT="$(cfg_get mysqlConfig port)"
REDIS_HOST="$(cfg_get redisConfig host)"; REDIS_PORT="$(cfg_get redisConfig port)"
FE_PORT=8080

# ---------- 加载 .env ----------
if [ -f .env ]; then
  set -a; . ./.env; set +a
  ok "已加载 .env"
fi

: "${DEEPSEEK_BASE_URL:=https://api.deepseek.com}"
: "${DEEPSEEK_MODEL_NAME:=deepseek-chat}"
export DEEPSEEK_BASE_URL DEEPSEEK_MODEL_NAME

# ---------- 环境检查 ----------
command -v go >/dev/null 2>&1 || { err "未找到 go，请先安装 Go 1.24+"; exit 1; }

if [ -z "${OPENAI_API_KEY:-}" ]; then
  warn "未设置 OPENAI_API_KEY，对话模型将无法调用（可写入 .env）"
fi
if [ -z "${DEEPSEEK_API_KEY:-}" ]; then
  warn "未设置 DEEPSEEK_API_KEY，将不生成会话摘要，仅按消息条数上限兜底"
fi

# 端口连通性探测：优先 nc，其次 bash 的 /dev/tcp
tcp_ready() {
  local host="$1" port="$2"
  if command -v nc >/dev/null 2>&1; then
    nc -z -w 2 "$host" "$port" >/dev/null 2>&1 && return 0
  fi
  (exec 3<>"/dev/tcp/${host}/${port}") >/dev/null 2>&1 && return 0
  return 1
}

if [ "$TARGET" != "web" ] && [ "$SKIP_CHECKS" -eq 0 ]; then
  if tcp_ready "${MYSQL_HOST:-127.0.0.1}" "${MYSQL_PORT:-3306}"; then
    ok "MySQL ${MYSQL_HOST}:${MYSQL_PORT} 可访问"
  else
    warn "MySQL ${MYSQL_HOST}:${MYSQL_PORT} 未连通，请确认已启动（若本环境不支持端口探测请忽略）"
  fi
  if tcp_ready "${REDIS_HOST:-127.0.0.1}" "${REDIS_PORT:-6379}"; then
    ok "Redis ${REDIS_HOST}:${REDIS_PORT} 可访问"
  else
    warn "Redis ${REDIS_HOST}:${REDIS_PORT} 未连通，验证码等功能会失败（若本环境不支持端口探测请忽略）"
  fi
fi

# ---------- 编译后端 ----------
BIN="bin/gopherai"
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*|Windows_NT) BIN="bin/gopherai.exe" ;;
esac

if [ "$TARGET" != "web" ]; then
  if [ "$NO_BUILD" -eq 1 ] && [ -x "$BIN" ]; then
    ok "跳过编译，使用已有二进制 $BIN"
  else
    info "编译后端 -> $BIN"
    mkdir -p bin
    go build -o "$BIN" .
    ok "编译完成"
  fi
fi

# ---------- 启动 ----------
BE_PID=""; FE_PID=""

cleanup() {
  trap - INT TERM EXIT
  if [ -n "$FE_PID" ]; then kill "$FE_PID" 2>/dev/null || true; fi
  if [ -n "$BE_PID" ]; then kill "$BE_PID" 2>/dev/null || true; fi
  wait 2>/dev/null || true
  info "已停止所有服务"
}
trap cleanup INT TERM EXIT

# 等待后端 HTTP 端口可访问；进程提前退出则返回失败
wait_backend() {
  local pid="$1" i
  for i in $(seq 1 30); do
    if curl -sS -o /dev/null --max-time 2 "http://127.0.0.1:${PORT}/" 2>/dev/null; then
      return 0
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      return 1
    fi
    sleep 1
  done
  return 1
}

if [ "$TARGET" != "web" ]; then
  info "启动后端 http://127.0.0.1:${PORT}"
  "$BIN" &
  BE_PID=$!
  if ! wait_backend "$BE_PID"; then
    err "后端启动失败，请查看上方日志"
    exit 1
  fi
  ok "后端已就绪 http://127.0.0.1:${PORT}"
fi

if [ "$TARGET" != "backend" ]; then
  NPM="npm"
  command -v "$NPM" >/dev/null 2>&1 || NPM="npm.cmd"
  command -v "$NPM" >/dev/null 2>&1 || { err "未找到 npm，请先安装 Node.js"; exit 1; }

  if [ ! -d vue-frontend/node_modules ]; then
    if [ "$NO_INSTALL" -eq 1 ]; then
      warn "缺少 vue-frontend/node_modules，跳过安装可能导致启动失败"
    else
      info "安装前端依赖（首次较慢）"
      (cd vue-frontend && "$NPM" install)
    fi
  fi

  info "启动前端 http://127.0.0.1:${FE_PORT}"
  (cd vue-frontend && "$NPM" run serve) &
  FE_PID=$!
  ok "前端启动中，请稍候访问 http://127.0.0.1:${FE_PORT}"
fi

info "Ctrl + C 停止服务"
wait
