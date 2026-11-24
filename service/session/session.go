package session

import (
	"GopherAI/common/aihelper"
	"GopherAI/common/code"
	"GopherAI/dao/message"
	"GopherAI/dao/session"
	"GopherAI/model"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
)

var ctx = context.Background()

// maxHistoryPageSize 历史消息单页最大条数
const maxHistoryPageSize = 100

// findSession 查询会话（包级变量，测试可替换）
var findSession = session.FindSessionByID

// checkSession 校验会话存在且归属指定用户，用于消息发送前的准入检查
func checkSession(userName string, sessionID string) code.Code {
	sess, err := findSession(ctx, sessionID)
	if err != nil {
		log.Println("checkSession FindSessionByID error:", err)
		return code.CodeServerBusy
	}
	if sess == nil {
		return code.CodeRecordNotFound
	}
	if sess.UserName != userName {
		return code.CodeForbidden
	}
	return code.CodeSuccess
}

// GetUserSessionsByUserName 从 DB 查询用户的会话列表（不依赖内存中的 AIHelper，兼容懒加载）
func GetUserSessionsByUserName(userName string) ([]model.SessionInfo, error) {
	sessions, err := session.GetSessionsByUserName(userName)
	if err != nil {
		return nil, err
	}

	sessionInfos := make([]model.SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		sessionInfos = append(sessionInfos, model.SessionInfo{
			SessionID: s.ID,
			Title:     s.Title,
		})
	}
	return sessionInfos, nil
}

func CreateSessionAndSendMessage(userName string, userQuestion string, modelType string) (string, string, code.Code) {
	//1：创建一个新的会话
	newSession := &model.Session{
		ID:       uuid.New().String(),
		UserName: userName,
		Title:    userQuestion, // 可以根据需求设置标题，这边暂时用用户第一次的问题作为标题
	}
	createdSession, err := session.CreateSession(newSession)
	if err != nil {
		log.Println("CreateSessionAndSendMessage CreateSession error:", err)
		return "", "", code.CodeServerBusy
	}

	//2：获取AIHelper并通过其管理消息
	manager := aihelper.GetGlobalManager()
	config := map[string]interface{}{
		"apiKey":   "your-api-key", // TODO: 从配置中获取
		"username": userName,       // 用于 RAG 模型获取用户文档
	}
	helper, err := manager.GetOrCreateAIHelper(userName, createdSession.ID, modelType, config)
	if err != nil {
		log.Println("CreateSessionAndSendMessage GetOrCreateAIHelper error:", err)
		return "", "", code.AIModelFail
	}

	//3：生成AI回复
	aiResponse, err_ := helper.GenerateResponse(userName, ctx, userQuestion)
	if err_ != nil {
		log.Println("CreateSessionAndSendMessage GenerateResponse error:", err_)
		return "", "", code.AIModelFail
	}

	return createdSession.ID, aiResponse.Content, code.CodeSuccess
}

func CreateStreamSessionOnly(userName string, userQuestion string) (string, code.Code) {
	newSession := &model.Session{
		ID:       uuid.New().String(),
		UserName: userName,
		Title:    userQuestion,
	}
	createdSession, err := session.CreateSession(newSession)
	if err != nil {
		log.Println("CreateStreamSessionOnly CreateSession error:", err)
		return "", code.CodeServerBusy
	}
	return createdSession.ID, code.CodeSuccess
}

func StreamMessageToExistingSession(userName string, sessionID string, userQuestion string, modelType string, writer http.ResponseWriter) code.Code {
	// 会话必须存在且归属当前用户：避免向任意 sessionId 静默写孤儿消息
	if c := checkSession(userName, sessionID); c != code.CodeSuccess {
		return c
	}

	// 确保 writer 支持 Flush
	flusher, ok := writer.(http.Flusher)
	if !ok {
		log.Println("StreamMessageToExistingSession: streaming unsupported")
		return code.CodeServerBusy
	}

	manager := aihelper.GetGlobalManager()
	config := map[string]interface{}{
		"apiKey":   "your-api-key", // TODO: 从配置中获取
		"username": userName,       // 用于 RAG 模型获取用户文档
	}
	helper, err := manager.GetOrCreateAIHelper(userName, sessionID, modelType, config)
	if err != nil {
		log.Println("StreamMessageToExistingSession GetOrCreateAIHelper error:", err)
		return code.AIModelFail
	}

	// 流式统计：只在流结束时输出一行汇总，避免逐 chunk 刷屏
	chunkCount, totalBytes := 0, 0
	start := time.Now()
	cb := func(msg string) {
		if msg == "" {
			return
		}
		// 按 JSON 下发，避免模型输出中的换行/空行破坏 SSE 帧（data: 行不能含裸换行）
		payload, err := json.Marshal(map[string]string{"delta": msg})
		if err != nil {
			log.Println("[SSE] marshal chunk error:", err)
			return
		}
		if _, err := writer.Write([]byte("data: " + string(payload) + "\n\n")); err != nil {
			log.Println("[SSE] Write error:", err)
			return
		}
		flusher.Flush() //  每次必须 flush
		chunkCount++
		totalBytes += len(msg)
	}

	_, err_ := helper.StreamResponse(userName, ctx, cb, userQuestion)
	if err_ != nil {
		log.Println("StreamMessageToExistingSession StreamResponse error:", err_)
		return code.AIModelFail
	}
	log.Printf("[SSE] finished session=%s chunks=%d bytes=%d cost=%dms",
		sessionID, chunkCount, totalBytes, time.Since(start).Milliseconds())

	_, err = writer.Write([]byte("data: [DONE]\n\n"))
	if err != nil {
		log.Println("StreamMessageToExistingSession write DONE error:", err)
		return code.AIModelFail
	}
	flusher.Flush()

	return code.CodeSuccess
}

func CreateStreamSessionAndSendMessage(userName string, userQuestion string, modelType string, writer http.ResponseWriter) (string, code.Code) {

	sessionID, code_ := CreateStreamSessionOnly(userName, userQuestion)
	if code_ != code.CodeSuccess {
		return "", code_
	}

	code_ = StreamMessageToExistingSession(userName, sessionID, userQuestion, modelType, writer)
	if code_ != code.CodeSuccess {

		return sessionID, code_
	}

	return sessionID, code.CodeSuccess
}

func ChatSend(userName string, sessionID string, userQuestion string, modelType string) (string, code.Code) {
	// 会话必须存在且归属当前用户：避免向任意 sessionId 静默写孤儿消息
	if c := checkSession(userName, sessionID); c != code.CodeSuccess {
		return "", c
	}

	//1：获取AIHelper
	manager := aihelper.GetGlobalManager()
	config := map[string]interface{}{
		"username": userName, // 用于 RAG 模型获取用户文档（若当前用户选择了RAG模型，该字段将会被用到）
	}
	helper, err := manager.GetOrCreateAIHelper(userName, sessionID, modelType, config)
	if err != nil {
		log.Println("ChatSend GetOrCreateAIHelper error:", err)
		return "", code.AIModelFail
	}

	//2：生成AI回复
	aiResponse, err_ := helper.GenerateResponse(userName, ctx, userQuestion)
	if err_ != nil {
		log.Println("ChatSend GenerateResponse error:", err_)
		return "", code.AIModelFail
	}

	return aiResponse.Content, code.CodeSuccess
}

// GetChatHistory 从 DB 查询会话历史（按时间升序）。
// pageSize <= 0 时返回全部；否则 page=1 为最新的一页，page 越大越早。
// beforeID > 0 时走游标分页：返回 id < beforeID 的更早消息，翻页期间追加新消息不会造成重复或漏读。
// 返回本页消息、消息总数与是否还有更早的消息。
func GetChatHistory(reqCtx context.Context, userName string, sessionID string, page, pageSize int, beforeID int64) ([]model.History, int64, bool, code.Code) {
	sess, err := findSession(reqCtx, sessionID)
	if err != nil {
		log.Println("GetChatHistory FindSessionByID error:", err)
		return nil, 0, false, code.CodeServerBusy
	}
	if sess == nil {
		return nil, 0, false, code.CodeRecordNotFound
	}
	if sess.UserName != userName {
		return nil, 0, false, code.CodeForbidden
	}

	total, err := message.CountBySessionID(reqCtx, sessionID)
	if err != nil {
		log.Println("GetChatHistory CountBySessionID error:", err)
		return nil, 0, false, code.CodeServerBusy
	}

	if beforeID > 0 {
		// 游标分页：以"已加载最早消息的 id"为锚点，不受新消息追加影响
		if pageSize <= 0 || pageSize > maxHistoryPageSize {
			pageSize = maxHistoryPageSize
		}
		msgs, err := message.ListBySessionIDBefore(reqCtx, sessionID, beforeID, pageSize)
		if err != nil {
			log.Println("GetChatHistory ListBySessionIDBefore error:", err)
			return nil, 0, false, code.CodeServerBusy
		}
		history := make([]model.History, 0, len(msgs))
		for _, msg := range msgs {
			history = append(history, model.History{ID: int64(msg.ID), IsUser: msg.IsUser, Content: msg.Content})
		}
		// 取满一页时可能还有更早的消息（下次游标请求取到 0 条时自然终止）
		return history, total, len(msgs) == pageSize, code.CodeSuccess
	}

	offset, limit := historyRange(int(total), page, pageSize)
	history := make([]model.History, 0, limit)
	if limit > 0 {
		msgs, err := message.ListBySessionIDRange(reqCtx, sessionID, offset, limit)
		if err != nil {
			log.Println("GetChatHistory ListBySessionIDRange error:", err)
			return nil, 0, false, code.CodeServerBusy
		}
		for _, msg := range msgs {
			history = append(history, model.History{ID: int64(msg.ID), IsUser: msg.IsUser, Content: msg.Content})
		}
	}
	return history, total, historyHasMore(int(total), page, pageSize), code.CodeSuccess
}

// historyRange 计算分页对应的 offset/limit（消息按 id 升序，page=1 为最新一页）
func historyRange(total, page, pageSize int) (int, int) {
	if pageSize <= 0 {
		return 0, total
	}
	if pageSize > maxHistoryPageSize {
		pageSize = maxHistoryPageSize
	}
	if page <= 0 {
		page = 1
	}
	end := total - (page-1)*pageSize
	if end <= 0 {
		return 0, 0
	}
	offset := end - pageSize
	if offset < 0 {
		return 0, end
	}
	return offset, pageSize
}

// historyHasMore 判断是否还有更早的历史页。
// pageSize 必须先按 maxHistoryPageSize 截断再参与计算：controller 侧曾用原始请求值计算，
// 导致 pageSize>100 时实际只返回 100 条却判定 hasMore=false，更早的历史永远翻不到
func historyHasMore(total, page, pageSize int) bool {
	if pageSize <= 0 {
		return false
	}
	if pageSize > maxHistoryPageSize {
		pageSize = maxHistoryPageSize
	}
	if page <= 0 {
		page = 1
	}
	return page*pageSize < total
}

func ChatStreamSend(userName string, sessionID string, userQuestion string, modelType string, writer http.ResponseWriter) code.Code {

	return StreamMessageToExistingSession(userName, sessionID, userQuestion, modelType, writer)
}
