<template>
  <div class="ai-chat-container">
    <!-- 左侧会话列表 -->
    <div class="session-list">
      <div class="session-list-header">
        <span>会话</span>
        <button class="new-chat-btn" @click="createNewSession">＋ 新会话</button>
      </div>
      <ul class="session-list-ul">
        <li
          v-for="session in sessions"
          :key="session.id"
          :class="['session-item', { active: currentSessionId === session.id }]"
          @click="switchSession(session.id)"
        >
          {{ session.name || `会话 ${session.id}` }}
        </li>
      </ul>
    </div>

    <!-- 右侧聊天区域 -->
    <div class="chat-section">
      <div class="top-bar">
        <button class="back-btn" @click="$router.push('/menu')">← 控制台</button>
        <div class="nameplate"><span class="led"></span><span class="nameplate-text">智能家居控制台</span></div>
        <div class="top-bar-spacer"></div>
        <button class="sync-btn" @click="syncHistory" :disabled="!currentSessionId || tempSession || loading">同步历史</button>
        <label for="modelType" class="field-label">控制模式</label>
        <select id="modelType" v-model="selectedModel" class="model-select">
          <option value="1">普通对话</option>
          <option value="3">MCP 设备控制</option>
        </select>
        <label for="streamingMode" class="field-label">
          <input type="checkbox" id="streamingMode" v-model="isStreaming" />
          流式
        </label>
      </div>

      <div class="chat-messages" ref="messagesRef">
        <div v-if="currentSession && currentSession.hasMore" class="load-more">
          <button
            class="load-more-btn"
            :disabled="currentSession.loadingMore"
            @click="loadMoreHistory"
          >
            {{ currentSession.loadingMore ? '加载中…' : '加载更早消息' }}
          </button>
        </div>
        <div
          v-for="(message, index) in currentMessages"
          :key="index"
          :class="['message', message.role === 'user' ? 'user-message' : 'ai-message']"
        >
          <div class="message-header">
            <b>{{ message.role === 'user' ? '指令' : '中枢' }}</b>
          </div>
          <div class="message-content" v-html="renderMarkdown(message.content)"></div>
        </div>
        <div v-if="streamMessage" class="message ai-message">
          <div class="message-header">
            <b>中枢</b>
            <span class="streaming-indicator">●</span>
          </div>
          <div class="message-content" v-html="renderMarkdown(streamMessage.content)"></div>
        </div>
        <div v-if="!currentMessages.length && !streamMessage" class="chat-empty">
          {{ tempSession ? '输入指令开始新的会话' : '这个会话还没有消息' }}
        </div>
      </div>

      <div class="chat-input">
        <textarea
          v-model="inputMessage"
          placeholder="输入指令，例如：把灯打开，窗帘拉开一半"
          @keydown.enter.exact.prevent="sendMessage"
          :disabled="loading"
          ref="messageInput"
          rows="1"
        ></textarea>
        <button
          type="button"
          :disabled="!inputMessage.trim() || loading"
          @click="sendMessage"
          class="send-btn"
        >
          {{ loading ? '发送中...' : '发送' }}
        </button>
      </div>
    </div>
  </div>
</template>

<script>


import { ref, reactive, nextTick, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import api from '../utils/api'

export default {
  name: 'AIChat',
  setup() {

    // 历史消息分页大小（后端单页上限 100）
    const HISTORY_PAGE_SIZE = 30

    // 会话集合：{ [sessionId]: { id, name, messages, loaded, page, total, hasMore, loadingMore } }
    const sessions = ref({})
    const currentSessionId = ref(null)
    const tempSession = ref(false)
    // 新会话在拿到后端 sessionId 之前的暂存消息，拿到 id 后整体移交给 sessions
    const tempMessages = ref([])
    const inputMessage = ref('')
    // 非流式请求正在等待响应的会话 key（sessionId 或 'temp'）
    const pendingKey = ref(null)
    // 当前流式任务：{ sessionKey, content, status }
    const streamState = ref(null)
    const messagesRef = ref(null)
    const messageInput = ref(null)
    const selectedModel = ref('3')
    const isStreaming = ref(false)
    // 当前流式请求的 AbortController（非响应式）
    let streamAbort = null

    // 当前视图对应的 key：'temp' 或 sessionId
    const activeKey = computed(() => (tempSession.value ? 'temp' : String(currentSessionId.value || '')))
    const currentSession = computed(() => (tempSession.value ? null : sessions.value[currentSessionId.value] || null))
    const currentMessages = computed(() => {
      if (tempSession.value) return tempMessages.value
      const sess = sessions.value[currentSessionId.value]
      return sess ? sess.messages : []
    })
    // 只有流式任务属于当前视图时才渲染"正在输出"的气泡
    const streamMessage = computed(() => {
      const s = streamState.value
      return s && s.sessionKey === activeKey.value ? s : null
    })
    const loading = computed(() => {
      if (pendingKey.value && pendingKey.value === activeKey.value) return true
      const s = streamState.value
      return Boolean(s && s.status === 'streaming' && s.sessionKey === activeKey.value)
    })

    const isStreamingFor = (key) => Boolean(
      streamState.value &&
      streamState.value.sessionKey === key &&
      streamState.value.status === 'streaming'
    )

    const createSessionState = (id, name) => ({
      id,
      name: name || `会话 ${id}`,
      messages: [],
      loaded: false,
      page: 0,
      total: 0,
      hasMore: false,
      loadingMore: false,
      // 游标分页锚点：已加载最早消息的 id（0 表示尚未从后端拉取过）
      cursorId: 0
    })

    const messagesArrayOf = (key) => {
      if (key === 'temp') return tempMessages.value
      const sess = sessions.value[key]
      return sess ? sess.messages : null
    }


    // 渲染前先做 HTML 转义：用户输入与模型输出都可能包含 <img onerror>、<script> 等标签，
    // 不转义直接进 v-html 会形成存储型 XSS（历史消息读取时再次触发）
    const escapeHtml = (text) => String(text)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;')

    const renderMarkdown = (text) => {
      if (!text && text !== '') return ''
      return escapeHtml(text)
        .replace(/\*\*(.*?)\*\*/g, '<strong>$1</strong>')
        .replace(/\*(.*?)\*/g, '<em>$1</em>')
        .replace(/`(.*?)`/g, '<code>$1</code>')
        .replace(/\n/g, '<br>')
    }

    const loadSessions = async () => {
      try {
        const response = await api.get('/AI/chat/sessions')
        if (response.data && response.data.status_code === 1000 && Array.isArray(response.data.sessions)) {
          const sessionMap = {}
          response.data.sessions.forEach(s => {
            const sid = String(s.sessionId)
            sessionMap[sid] = createSessionState(sid, s.name)
          })
          sessions.value = sessionMap
        }
      } catch (error) {
        console.error('Load sessions error:', error)
      }
    }

    // 拉取指定页历史：page=1 为最新一页。同时记录游标锚点（本页最早消息的 id），
    // 供"加载更早消息"走游标分页，避免翻页期间新消息追加导致页间重复/漏读
    const fetchHistoryPage = async (sid, page) => {
      const sess = sessions.value[sid]
      if (!sess) return null
      const response = await api.post('/AI/chat/history', {
        sessionId: sid,
        page,
        pageSize: HISTORY_PAGE_SIZE
      })
      if (!response.data || response.data.status_code !== 1000 || !Array.isArray(response.data.history)) {
        throw new Error((response.data && response.data.status_msg) || '获取历史失败')
      }
      const fetched = response.data.history.map(item => ({
        id: Number(item.id || 0),
        role: item.is_user ? 'user' : 'assistant',
        content: item.content
      }))
      // fetched 为升序，第一条是本页最早的消息
      sess.cursorId = fetched.length ? fetched[0].id : 0
      // page>1 是更早的消息，拼接到头部
      sess.messages = page <= 1 ? fetched : [...fetched, ...sess.messages]
      sess.page = page
      sess.total = Number(response.data.total || 0)
      sess.hasMore = Boolean(response.data.hasMore)
      sess.loaded = true
      return sess
    }

    // 游标分页：拉取 id < cursorId 的更早消息并拼到头部。
    // 以消息 id 为锚点，翻页期间后台流式写入新消息不会造成重复或漏读
    const fetchHistoryBefore = async (sid, beforeId) => {
      const sess = sessions.value[sid]
      if (!sess || !beforeId) return null
      const response = await api.post('/AI/chat/history', {
        sessionId: sid,
        beforeId,
        pageSize: HISTORY_PAGE_SIZE
      })
      if (!response.data || response.data.status_code !== 1000 || !Array.isArray(response.data.history)) {
        throw new Error((response.data && response.data.status_msg) || '获取历史失败')
      }
      const fetched = response.data.history.map(item => ({
        id: Number(item.id || 0),
        role: item.is_user ? 'user' : 'assistant',
        content: item.content
      }))
      // 防御：仅保留确实早于当前锚点的消息，杜绝任何来源的重复
      const older = fetched.filter(m => m.id && m.id < beforeId)
      if (older.length) {
        sess.cursorId = older[0].id
        sess.messages = [...older, ...sess.messages]
      }
      sess.hasMore = Boolean(response.data.hasMore)
      return sess
    }

    // 新会话拿到后端 sessionId：暂存消息整体移交（引用转移，不复制）
    const bindTempSession = (sid) => {
      const messages = tempMessages.value
      const existing = sessions.value[sid]
      if (existing) {
        existing.messages = messages
      } else {
        sessions.value[sid] = {
          ...createSessionState(sid, '新会话'),
          messages,
          loaded: true,
          page: 1,
          total: messages.length,
          hasMore: false
        }
      }
      tempMessages.value = []
      return sessions.value[sid]
    }

    const createNewSession = () => {
      // 上一个新会话还在流式中：中断，避免把内容写进新的空会话
      if (streamState.value && streamState.value.sessionKey === 'temp') {
        if (streamAbort) {
          streamAbort.abort()
          streamAbort = null
        }
        streamState.value = null
      }
      currentSessionId.value = 'temp'
      tempSession.value = true
      tempMessages.value = []
      // focus input
      nextTick(() => {
        if (messageInput.value) messageInput.value.focus()
      })
    }

    const switchSession = async (sessionId) => {
      const sid = String(sessionId)
      const sess = sessions.value[sid]
      if (!sess) return
      if (!tempSession.value && currentSessionId.value === sid) {
        await nextTick()
        scrollToBottom()
        return
      }

      // 不中断后台流式任务：它只写自己会话的数组，不会串到当前视图
      currentSessionId.value = sid
      tempSession.value = false

      // 首次进入该会话时拉最新一页；正在流式输出的会话保持本地内容，避免被 DB 覆盖
      if (!sess.loaded && !sess.loadingMore && !isStreamingFor(sid)) {
        sess.loadingMore = true
        try {
          await fetchHistoryPage(sid, 1)
        } catch (err) {
          console.error('Load history error:', err)
          ElMessage.error('加载历史失败')
        } finally {
          sess.loadingMore = false
        }
      }

      await nextTick()
      scrollToBottom()
    }

    const syncHistory = async () => {
      const sess = currentSession.value
      if (!sess) {
        ElMessage.warning('请选择已有会话进行同步')
        return
      }
      if (sess.loadingMore) return
      if (isStreamingFor(sess.id)) {
        ElMessage.warning('当前会话正在生成回复，请稍后再同步')
        return
      }

      const el = messagesRef.value
      const prevHeight = el ? el.scrollHeight : 0
      const prevTop = el ? el.scrollTop : 0
      const atBottom = el ? el.scrollHeight - el.scrollTop - el.clientHeight < 40 : true

      sess.loadingMore = true
      try {
        await fetchHistoryPage(sess.id, 1)
        await nextTick()
        if (atBottom) {
          scrollToBottom()
        } else if (el) {
          // 重新加载第一页后保持用户当前的阅读位置
          el.scrollTop = el.scrollHeight - prevHeight + prevTop
        }
      } catch (err) {
        console.error('Sync history error:', err)
        ElMessage.error('请求历史数据失败')
      } finally {
        sess.loadingMore = false
      }
    }

    // 向上翻页：以游标（最早消息 id）加载更早的一页并保持滚动位置不跳动
    const loadMoreHistory = async () => {
      const sess = currentSession.value
      if (!sess || !sess.hasMore || sess.loadingMore) return

      const el = messagesRef.value
      const prevHeight = el ? el.scrollHeight : 0
      const prevTop = el ? el.scrollTop : 0

      sess.loadingMore = true
      try {
        await fetchHistoryBefore(sess.id, sess.cursorId)
        await nextTick()
        if (el) el.scrollTop = el.scrollHeight - prevHeight + prevTop
      } catch (err) {
        console.error('Load more history error:', err)
        ElMessage.error('加载更早消息失败')
      } finally {
        sess.loadingMore = false
      }
    }


    // 流式收尾：把累积的内容落到目标会话的消息数组里（与视图无关，切走了也不会写错会话）
    const finalizeStream = (state, keepContent) => {
      if (streamState.value === state) streamState.value = null
      const text = state.content
      const arr = messagesArrayOf(state.sessionKey)
      if (!arr || !keepContent || !text) return
      arr.push({ role: 'assistant', content: text })
      if (state.sessionKey !== 'temp') {
        const sess = sessions.value[state.sessionKey]
        if (sess) sess.total += 1
      }
      if (state.sessionKey === activeKey.value) {
        nextTick(() => scrollToBottom())
      }
    }

    // 流被中断（连接断开 / 未收到 [DONE] 就 EOF）：保留已生成的部分内容并明确提示，
    // 不能当作正常完成静默收尾（截断的回答会被误认为完整结果）
    const handleInterruptedStream = (state) => {
      if (streamState.value === state) streamState.value = null
      const partial = state.content
      if (partial) {
        const arr = messagesArrayOf(state.sessionKey)
        if (arr) arr.push({ role: 'assistant', content: partial })
        const sess = state.sessionKey === 'temp' ? null : sessions.value[state.sessionKey]
        if (sess) sess.total += 1
        ElMessage.error('流式传输中断，已保留已生成内容')
      } else {
        ElMessage.error('流式连接中断，未收到任何内容')
      }
    }

    const sendMessage = async () => {
      const question = inputMessage.value.trim()
      if (!question) {
        ElMessage.warning('请输入消息内容')
        return
      }
      if (loading.value) return
      if (!tempSession.value && !currentSession.value) {
        ElMessage.warning('请先选择或新建会话')
        return
      }

      // 记住本轮消息属于哪个会话，后续所有写入都以它为准
      const startKey = activeKey.value
      const target = messagesArrayOf(startKey)
      inputMessage.value = ''
      target.push({ role: 'user', content: question })
      if (currentSession.value) currentSession.value.total += 1

      await nextTick()
      scrollToBottom()

      try {
        if (isStreaming.value) {
          await handleStreaming(question, startKey)
        } else {
          await handleNormal(question, startKey)
        }
      } catch (err) {
        console.error('Send message error:', err)
        if (!err || err.name !== 'AbortError') {
          ElMessage.error((err && err.message) || '发送失败，请重试')
        }
        // 回滚本轮用户消息（若后端已落库，可用"同步历史"纠正）
        const arr = messagesArrayOf(startKey)
        if (arr && arr.length && arr[arr.length - 1].role === 'user') {
          arr.pop()
          const sess = startKey === 'temp' ? null : sessions.value[startKey]
          if (sess) sess.total = Math.max(0, sess.total - 1)
        }
      } finally {
        await nextTick()
        scrollToBottom()
      }
    }

    async function handleStreaming(question, startKey) {
      const isTempStart = startKey === 'temp'
      // 流式状态与视图绑定解耦：只记录归属的 sessionKey，切换会话不会写错数组
      // 必须是 reactive：直接改原始对象不会触发视图更新，且 ref 里存放的是深层代理，
      // 用原始对象做 === 比较会永远失败（导致 streamState 清不掉、按钮一直停在“发送中”）
      const state = reactive({ sessionKey: startKey, content: '', status: 'streaming' })
      const controller = new AbortController()
      streamAbort = controller
      streamState.value = state

      const url = isTempStart
        ? '/api/AI/chat/send-stream-new-session'
        : '/api/AI/chat/send-stream'
      const body = isTempStart
        ? { question: question, modelType: selectedModel.value }
        : { question: question, modelType: selectedModel.value, sessionId: startKey }

      try {
        // 创建 fetch 连接读取 SSE 流
        const response = await fetch(url, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
          signal: controller.signal
        })

        if (!response.ok || !response.body) throw new Error('流式请求失败')

        const reader = response.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''

        // 追加一段增量文本：state 是 reactive，改它即可触发增量渲染
        const appendChunk = async (text) => {
          if (!text) return
          state.content += text
          // 只有正在看这个会话时才滚动，后台流不打扰用户
          if (state.sessionKey === activeKey.value) {
            await nextTick()
            scrollToBottom()
          }
        }

        // 收到 [DONE] 即视为结束，不再依赖代理及时关闭连接
        let finished = false
        // eslint-disable-next-line no-constant-condition
        while (!finished) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })
          // 按行分割，保留未完成的行
          const lines = buffer.split('\n')
          buffer = lines.pop() || ''

          for (const line of lines) {
            const trimmedLine = line.trim()
            if (!trimmedLine.startsWith('data:')) continue
            // SSE 格式：data: <json|content>
            const data = trimmedLine.slice(5).trim()
            if (!data) continue
            if (data === '[DONE]') {
              finished = true
              break
            }

            let parsed = null
            if (data.startsWith('{')) {
              try {
                parsed = JSON.parse(data)
              } catch (e) {
                parsed = null // 不是 JSON，当作正文
              }
            }

            if (parsed) {
              // 新会话首次拿到 sessionId：把暂存消息移交给会话集合
              if (parsed.sessionId && state.sessionKey === 'temp') {
                const newSid = String(parsed.sessionId)
                const sess = bindTempSession(newSid)
                if (!sess.name || sess.name === '新会话') sess.name = question
                // 用户若已切到别的会话，不要把他拽回新会话
                if (tempSession.value) {
                  currentSessionId.value = newSid
                  tempSession.value = false
                }
                state.sessionKey = newSid
                continue
              }
              if (parsed.message) throw new Error(String(parsed.message))
              if (typeof parsed.delta === 'string') {
                await appendChunk(parsed.delta)
              }
              continue
            }

            await appendChunk(data)
          }
        }

        if (finished) {
          // 主动断开，避免连接未被及时关闭时一直挂起
          try {
            await reader.cancel()
          } catch (e) {
            // ignore
          }
          finalizeStream(state, true)
        } else {
          // 连接在收到 [DONE] 之前就关闭（代理/后端/网络中断）：
          // 视为中断而不是正常完成，截断的内容要明确提示
          handleInterruptedStream(state)
        }
      } catch (err) {
        if (err && err.name === 'AbortError') {
          finalizeStream(state, false)
          return
        }
        console.error('Stream error:', err)
        if (!state.content) throw err // 没有内容则交给 sendMessage 回滚用户消息
        handleInterruptedStream(state)
      } finally {
        if (streamAbort === controller) streamAbort = null
      }
    }

    async function handleNormal(question, startKey) {
      const isTempStart = startKey === 'temp'
      pendingKey.value = startKey
      try {
        if (isTempStart) {
          const response = await api.post('/AI/chat/send-new-session', {
            question: question,
            modelType: selectedModel.value
          })
          if (!response.data || response.data.status_code !== 1000) {
            throw new Error((response.data && response.data.status_msg) || '发送失败')
          }
          const newSid = String(response.data.sessionId)
          const sess = bindTempSession(newSid)
          sess.name = question
          sess.messages.push({ role: 'assistant', content: response.data.Information || '' })
          sess.total = sess.messages.length
          if (tempSession.value) {
            currentSessionId.value = newSid
            tempSession.value = false
          }
        } else {
          const response = await api.post('/AI/chat/send', {
            question: question,
            modelType: selectedModel.value,
            sessionId: startKey
          })
          if (!response.data || response.data.status_code !== 1000) {
            throw new Error((response.data && response.data.status_msg) || '发送失败')
          }
          const sess = sessions.value[startKey]
          if (sess) {
            sess.messages.push({ role: 'assistant', content: response.data.Information || '' })
            sess.total += 1
          }
        }
        await nextTick()
        scrollToBottom()
      } finally {
        if (pendingKey.value === startKey) pendingKey.value = null
      }
    }


    const scrollToBottom = () => {
      if (messagesRef.value) {
        try {
          messagesRef.value.scrollTop = messagesRef.value.scrollHeight
        } catch (e) {
          // ignore
        }
      }
    }

    onMounted(() => {
      loadSessions()
    })

    // expose to template
    return {
      sessions: computed(() => Object.values(sessions.value)),
      currentSessionId,
      tempSession,
      currentSession,
      currentMessages,
      streamMessage,
      inputMessage,
      loading,
      messagesRef,
      messageInput,
      selectedModel,
      isStreaming,
      renderMarkdown,
      createNewSession,
      switchSession,
      syncHistory,
      loadMoreHistory,
      sendMessage
    }
  }
}
</script>

<style scoped>
/* ===== 实体控制台设计令牌 ===== */
.ai-chat-container {
  --body: #24272c;        /* 机身 */
  --body-deep: #1b1e22;   /* 机身暗部（侧栏/顶栏/输入条） */
  --screen: #121519;      /* 内嵌屏幕 */
  --ink: #e4e8ed;         /* 屏上文字 */
  --ink-dim: #8b939e;     /* 屏上次级文字 */
  --amber: #e8a33d;       /* 琥珀指示 */
  --amber-deep: #b97f24;
  --green: #43d17c;       /* LED 绿 */
  --line: rgba(255, 255, 255, 0.08);

  height: 100vh;
  display: flex;
  background: var(--body);
  color: var(--ink);
  overflow: hidden;
  font-family: "PingFang SC", "Microsoft YaHei", "Helvetica Neue", Arial, sans-serif;
}

/* 铭牌刻字：等宽 + 大字距 */
.session-list-header > span,
.field-label,
.nameplate-text,
.message-header b {
  font-family: Consolas, "Courier New", monospace;
  letter-spacing: 0.14em;
  font-size: 11px;
  font-weight: 700;
  text-transform: uppercase;
}

/* ===== 左侧：会话通道 ===== */
.session-list {
  width: 232px;
  height: 100vh;
  display: flex;
  flex-direction: column;
  background: var(--body-deep);
  border-right: 1px solid #000;
  box-shadow: inset -1px 0 0 rgba(255, 255, 255, 0.04);
}

.session-list-header {
  padding: 16px 14px 14px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  align-items: stretch;
  border-bottom: 1px solid var(--line);
}

.session-list-header > span {
  color: var(--ink-dim);
  text-align: left;
  padding-left: 2px;
}

.new-chat-btn {
  width: 100%;
  padding: 10px 0;
  cursor: pointer;
  background: #2f333a;
  color: var(--ink);
  border: 1px solid #101215;
  border-bottom: 3px solid #101215;
  border-radius: 6px;
  font-size: 13px;
  font-weight: 600;
  transition: transform 0.08s ease, border-bottom-width 0.08s ease, background 0.15s ease;
}

.new-chat-btn:hover {
  background: #383d46;
}

.new-chat-btn:active {
  transform: translateY(2px);
  border-bottom-width: 1px;
}

.session-list-ul {
  list-style: none;
  padding: 6px 0;
  margin: 0;
  flex: 1;
  overflow-y: auto;
}

.session-item {
  padding: 11px 16px;
  cursor: pointer;
  color: var(--ink-dim);
  font-size: 13px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  border-left: 3px solid transparent;
  transition: background 0.15s ease, color 0.15s ease;
}

.session-item:hover {
  background: rgba(255, 255, 255, 0.04);
  color: var(--ink);
}

.session-item.active {
  background: rgba(232, 163, 61, 0.1);
  color: var(--amber);
  border-left-color: var(--amber);
}

/* ===== 右侧主区 ===== */
.chat-section {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}

.top-bar {
  background: var(--body-deep);
  border-bottom: 1px solid #000;
  box-shadow: inset 0 -1px 0 rgba(255, 255, 255, 0.04);
  display: flex;
  align-items: center;
  padding: 10px 16px;
  gap: 14px;
}

.top-bar-spacer {
  flex: 1;
}

.nameplate {
  display: flex;
  align-items: center;
  gap: 8px;
}

.nameplate-text {
  color: var(--ink);
}

.led {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--green);
  box-shadow: 0 0 6px var(--green);
  animation: ledBreathe 2.4s ease-in-out infinite;
}

@keyframes ledBreathe {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.45; }
}

.back-btn,
.sync-btn {
  background: #2f333a;
  color: var(--ink);
  border: 1px solid #101215;
  border-bottom: 3px solid #101215;
  border-radius: 6px;
  padding: 7px 13px;
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
  transition: transform 0.08s ease, border-bottom-width 0.08s ease, background 0.15s ease;
}

.back-btn:hover,
.sync-btn:hover:not(:disabled) {
  background: #383d46;
}

.back-btn:active,
.sync-btn:active:not(:disabled) {
  transform: translateY(2px);
  border-bottom-width: 1px;
}

.sync-btn:disabled {
  color: #5a616b;
  background: #26292e;
  cursor: not-allowed;
}

.field-label {
  color: var(--ink-dim);
  display: flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
}

.model-select {
  padding: 6px 10px;
  background: var(--screen);
  border: 1px solid #000;
  box-shadow: inset 0 1px 4px rgba(0, 0, 0, 0.5);
  border-radius: 4px;
  color: var(--ink);
  font-size: 13px;
  cursor: pointer;
}

/* ===== 内嵌屏幕：消息区 ===== */
.chat-messages {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  margin: 14px;
  padding: 22px 24px;
  display: flex;
  flex-direction: column;
  gap: 14px;
  background: var(--screen);
  border-radius: 8px;
  border: 1px solid #000;
  box-shadow: inset 0 2px 14px rgba(0, 0, 0, 0.6);
}

.chat-messages::-webkit-scrollbar {
  width: 8px;
}
.chat-messages::-webkit-scrollbar-thumb {
  background: #2c3138;
  border-radius: 4px;
}
.chat-messages::-webkit-scrollbar-track {
  background: transparent;
}

/* ===== 历史分页 ===== */
.load-more {
  display: flex;
  justify-content: center;
  padding-bottom: 4px;
}

.load-more-btn {
  padding: 6px 16px;
  background: transparent;
  color: var(--ink-dim);
  border: 1px dashed rgba(255, 255, 255, 0.18);
  border-radius: 20px;
  font-size: 12px;
  font-family: Consolas, "Courier New", monospace;
  letter-spacing: 0.08em;
  cursor: pointer;
  transition: color 0.15s ease, border-color 0.15s ease;
}

.load-more-btn:hover:not(:disabled) {
  color: var(--amber);
  border-color: rgba(232, 163, 61, 0.5);
}

.load-more-btn:disabled {
  color: #5a616b;
  border-color: rgba(255, 255, 255, 0.08);
  cursor: not-allowed;
}

.chat-empty {
  margin: auto;
  color: #5a616b;
  font-size: 13px;
  letter-spacing: 0.06em;
}

.message {
  max-width: 72%;
  padding: 10px 14px;
  border-radius: 4px;
  line-height: 1.7;
  word-wrap: break-word;
  font-size: 14.5px;
  box-sizing: border-box;
}

.user-message {
  align-self: flex-end;
  background: rgba(232, 163, 61, 0.08);
  border: 1px solid rgba(232, 163, 61, 0.32);
  border-left: 3px solid var(--amber);
  color: #f0ddba;
}

.ai-message {
  align-self: flex-start;
  background: rgba(255, 255, 255, 0.03);
  border: 1px solid var(--line);
  border-left: 3px solid #4a525d;
  color: var(--ink);
}

.message-header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.user-message .message-header b {
  color: var(--amber);
}

.ai-message .message-header b {
  color: var(--ink-dim);
}

.streaming-indicator {
  color: var(--amber);
  font-size: 9px;
  animation: ledBreathe 1s ease-in-out infinite;
}

.message-content {
  white-space: pre-wrap;
  word-break: break-word;
}

.message-content code {
  background: rgba(255, 255, 255, 0.08);
  padding: 1px 5px;
  border-radius: 3px;
  font-family: Consolas, "Courier New", monospace;
  font-size: 13px;
}

/* ===== 输入条 ===== */
.chat-input {
  padding: 12px 16px 14px;
  background: var(--body-deep);
  border-top: 1px solid #000;
  position: relative;
}

.chat-input textarea {
  width: 100%;
  resize: none;
  border: 1px solid #000;
  border-radius: 6px;
  padding: 12px 104px 12px 14px;
  font-size: 14.5px;
  outline: none;
  background: var(--screen);
  color: var(--ink);
  min-height: 20px;
  max-height: 160px;
  box-shadow: inset 0 2px 8px rgba(0, 0, 0, 0.5);
  transition: border-color 0.15s ease;
}

.chat-input textarea::placeholder {
  color: #5a616b;
}

.chat-input textarea:focus {
  border-color: rgba(232, 163, 61, 0.45);
}

.send-btn {
  position: absolute;
  right: 26px;
  bottom: 20px;
  padding: 9px 20px;
  border: none;
  border-bottom: 3px solid var(--amber-deep);
  border-radius: 6px;
  background: var(--amber);
  color: #1c1403;
  font-size: 14px;
  font-weight: 700;
  cursor: pointer;
  transition: transform 0.08s ease, border-bottom-width 0.08s ease, background 0.15s ease;
}

.send-btn:hover:not(:disabled) {
  background: #f0b456;
}

.send-btn:active:not(:disabled) {
  transform: translateY(2px);
  border-bottom-width: 1px;
}

.send-btn:disabled {
  background: #3a3f47;
  color: #6b727c;
  border-bottom-color: #26292e;
  cursor: not-allowed;
}

/* ===== 窄屏适配：侧栏变横向通道条 ===== */
@media (max-width: 768px) {
  .ai-chat-container {
    flex-direction: column;
  }

  .session-list {
    width: 100%;
    height: auto;
    flex-direction: row;
    align-items: center;
    border-right: none;
    border-bottom: 1px solid #000;
    overflow: hidden;
  }

  .session-list-header {
    flex-direction: row;
    align-items: center;
    gap: 10px;
    padding: 8px 10px;
    border-bottom: none;
  }

  .session-list-header > span {
    display: none;
  }

  .new-chat-btn {
    width: auto;
    padding: 8px 12px;
    white-space: nowrap;
  }

  .session-list-ul {
    display: flex;
    flex-direction: row;
    overflow-x: auto;
    padding: 0 6px;
  }

  .session-item {
    white-space: nowrap;
    padding: 8px 12px;
    border-left: none;
    border-bottom: 2px solid transparent;
  }

  .session-item.active {
    border-left: none;
    border-bottom-color: var(--amber);
  }

  .top-bar {
    flex-wrap: wrap;
    gap: 8px;
    padding: 8px 10px;
  }

  .nameplate {
    display: none;
  }

  .chat-messages {
    margin: 10px;
    padding: 14px;
  }

  .message {
    max-width: 90%;
  }
}
</style>
