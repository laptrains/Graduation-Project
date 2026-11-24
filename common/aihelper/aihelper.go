package aihelper

import (
	"GopherAI/common/messagewriter"
	"GopherAI/model"
	"GopherAI/pkg/memory"
	"GopherAI/utils"
	"context"
	"log"
)

// AIHelper AI助手结构体，一个会话绑定一个 AIHelper，持有有界会话记忆和 AI 模型
type AIHelper struct {
	model  AIModel
	memory *memory.SessionMemory
	//一个会话绑定一个AIHelper
	SessionID string
	userName  string
	saveFunc  func(*model.Message) (*model.Message, error)
}

// NewAIHelper 创建新的AIHelper实例，mem 为该会话已加载的记忆
func NewAIHelper(model_ AIModel, SessionID string, userName string, mem *memory.SessionMemory) *AIHelper {
	return &AIHelper{
		model:    model_,
		memory:   mem,
		userName: userName,
		// 异步投递到批量写入器，由后台协程批量落库，避免每条消息同步打 DB。
		saveFunc: func(msg *model.Message) (*model.Message, error) {
			if !messagewriter.GetWriter().Enqueue(msg) {
				log.Printf("[aihelper] messagewriter stopped, message dropped: session=%s", msg.SessionID)
			}
			return msg, nil
		},
		SessionID: SessionID,
	}
}

// AddMessage 添加消息到会话记忆，Save 为 true 时同时持久化
func (a *AIHelper) AddMessage(Content string, UserName string, IsUser bool, Save bool) {
	a.memory.Append(memory.Turn{IsUser: IsUser, Content: Content})
	if !Save {
		return
	}
	a.saveFunc(&model.Message{
		SessionID: a.SessionID,
		Content:   Content,
		UserName:  UserName,
		IsUser:    IsUser,
	})
}

// SetSaveFunc 设置消息存储函数（通过回调函数避免循环依赖）
// 通过传入func，自己调用外部的保存函数，即可支持同步异步等多种策略
func (a *AIHelper) SetSaveFunc(saveFunc func(*model.Message) (*model.Message, error)) {
	a.saveFunc = saveFunc
}

// GetMessages 获取当前近期窗口的消息快照（不含已被摘要的历史，完整历史请查 DB）
func (a *AIHelper) GetMessages() []*model.Message {
	window := a.memory.Window()
	out := make([]*model.Message, 0, len(window))
	for _, t := range window {
		out = append(out, &model.Message{SessionID: a.SessionID, Content: t.Content, IsUser: t.IsUser})
	}
	return out
}

// GenerateResponse 同步生成
func (a *AIHelper) GenerateResponse(userName string, ctx context.Context, userQuestion string) (*model.Message, error) {
	a.AddMessage(userQuestion, userName, true, true)

	//注入的是「摘要 + 近期窗口」，而非全量历史
	schemaMsg, err := a.model.GenerateResponse(ctx, a.memory.BuildContext())
	if err != nil {
		return nil, err
	}

	modelMsg := utils.ConvertToModelMessage(a.SessionID, userName, schemaMsg)
	a.AddMessage(modelMsg.Content, userName, false, true)
	a.maybeExtract()
	a.maybeCompress()

	return modelMsg, nil
}

// StreamResponse 流式生成
func (a *AIHelper) StreamResponse(userName string, ctx context.Context, cb StreamCallback, userQuestion string) (*model.Message, error) {
	a.AddMessage(userQuestion, userName, true, true)

	content, err := a.model.StreamResponse(ctx, a.memory.BuildContext(), cb)
	if err != nil {
		return nil, err
	}
	modelMsg := &model.Message{
		SessionID: a.SessionID,
		UserName:  userName,
		Content:   content,
		IsUser:    false,
	}
	a.AddMessage(modelMsg.Content, userName, false, true)
	a.maybeExtract()
	a.maybeCompress()

	return modelMsg, nil
}

// maybeExtract 未提取消息达到阈值时异步提取长期记忆，与摘要相互独立、不阻塞本次响应
func (a *AIHelper) maybeExtract() {
	if !a.memory.NeedExtract() {
		return
	}
	e := getExtractor()
	if e == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), a.memory.Config().SummarizeTimeout)
		defer cancel()
		if err := a.memory.Extract(ctx, e); err != nil {
			log.Printf("[aihelper] session=%s user=%s extract failed, will retry next turn: %v", a.SessionID, a.userName, err)
		}
	}()
}

// maybeCompress 窗口超阈值时异步生成滚动摘要，不阻塞本次响应；
// 使用独立 context，不受 HTTP 请求取消影响
func (a *AIHelper) maybeCompress() {
	if !a.memory.NeedCompress() {
		return
	}
	s := getSummarizer()
	if s == nil {
		log.Printf("[memory] session=%s compress skipped: summarizer disabled (set DEEPSEEK_API_KEY to enable)", a.SessionID)
		return
	}
	go func() {
		log.Printf("[memory] session=%s compress started, window=%d", a.SessionID, len(a.memory.Window()))
		ctx, cancel := context.WithTimeout(context.Background(), a.memory.Config().SummarizeTimeout)
		defer cancel()
		if err := a.memory.Compress(ctx, s); err != nil {
			log.Printf("[aihelper] session=%s compress failed, fallback to window limit: %v", a.SessionID, err)
		}
	}()
}

// GetModelType 获取模型类型
func (a *AIHelper) GetModelType() string {
	return a.model.GetModelType()
}
