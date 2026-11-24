package aihelper

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// fakeAIModel 最小 AIModel 实现，记录 modelType 与 Close 调用
type fakeAIModel struct {
	modelType string
	closed    bool
}

func (f *fakeAIModel) GenerateResponse(context.Context, []*schema.Message) (*schema.Message, error) {
	return schema.AssistantMessage("ok", nil), nil
}

func (f *fakeAIModel) StreamResponse(context.Context, []*schema.Message, StreamCallback) (string, error) {
	return "ok", nil
}

func (f *fakeAIModel) GetModelType() string { return f.modelType }

func (f *fakeAIModel) Close() { f.closed = true }

// newTestManager 构造注入了 fake loader 的 manager（不触碰 DB / 全局 factory）
func newTestManager() *AIHelperManager {
	m := NewAIHelperManager()
	m.loadHelper = func(_ context.Context, _, sessionID, modelType string, _ map[string]interface{}) (*AIHelper, error) {
		return NewAIHelper(&fakeAIModel{modelType: modelType}, sessionID, "tester", nil), nil
	}
	return m
}

// #2 同一会话切换 modelType 必须重建 helper 并释放旧 helper 的资源（如 MCP 连接）
func TestManager_ModelTypeSwitchReplacesHelper(t *testing.T) {
	m := newTestManager()

	h1, err := m.GetOrCreateAIHelper("tester", "sess-1", "1", nil)
	if err != nil {
		t.Fatal(err)
	}
	f1 := h1.model.(*fakeAIModel)

	// 前端把同一会话切到 MCP 设备控制模式（type 3）
	h2, err := m.GetOrCreateAIHelper("tester", "sess-1", "3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h2 == h1 {
		t.Fatal("switching modelType must create a new helper, got the cached one")
	}
	if h2.ModelType() != "3" {
		t.Fatalf("new helper should use modelType 3, got %s", h2.ModelType())
	}
	if !f1.closed {
		t.Fatal("old helper must be closed after being replaced (MCP client would leak)")
	}

	// 再次请求相同 modelType：返回新 helper 的缓存
	h3, err := m.GetOrCreateAIHelper("tester", "sess-1", "3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h3 != h2 {
		t.Fatal("same modelType should return the cached helper")
	}
}

// 相同 modelType 重复请求不重建
func TestManager_SameModelTypeReturnsCachedHelper(t *testing.T) {
	m := newTestManager()

	h1, err := m.GetOrCreateAIHelper("tester", "sess-1", "1", nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := m.GetOrCreateAIHelper("tester", "sess-1", "1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("same modelType should return the cached helper")
	}
}

// 移除会话时应释放 helper 资源
func TestManager_RemoveClosesHelper(t *testing.T) {
	m := newTestManager()
	h, err := m.GetOrCreateAIHelper("tester", "sess-1", "1", nil)
	if err != nil {
		t.Fatal(err)
	}
	f := h.model.(*fakeAIModel)

	m.RemoveAIHelper("tester", "sess-1")
	if !f.closed {
		t.Fatal("RemoveAIHelper should close the helper's model resources")
	}
	if _, ok := m.GetAIHelper("tester", "sess-1"); ok {
		t.Fatal("helper should be removed from cache")
	}
}

// 不同会话互不影响
func TestManager_SessionsAreIndependent(t *testing.T) {
	m := newTestManager()
	h1, err := m.GetOrCreateAIHelper("tester", "sess-1", "1", nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := m.GetOrCreateAIHelper("tester", "sess-2", "3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("different sessions must have different helpers")
	}
}
