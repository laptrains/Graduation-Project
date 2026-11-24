package session

import (
	"context"
	"net/http"
	"testing"

	"GopherAI/common/code"
	"GopherAI/model"
)

// withStubSession 临时替换会话查询（包级变量注入，测试后恢复）
func withStubSession(t *testing.T, fn func(context.Context, string) (*model.Session, error)) {
	t.Helper()
	orig := findSession
	findSession = fn
	t.Cleanup(func() { findSession = orig })
}

func Test_historyRange(t *testing.T) {
	cases := []struct {
		name                  string
		total, page, pageSize int
		wantOffset, wantLimit int
	}{
		{"all", 25, 0, 0, 0, 25},
		{"latest page", 25, 1, 10, 15, 10},
		{"middle page", 25, 2, 10, 5, 10},
		{"last partial page", 25, 3, 10, 0, 5},
		{"beyond", 25, 4, 10, 0, 0},
		{"page default", 25, 0, 10, 15, 10},
		{"page size capped", 300, 1, 1000, 200, maxHistoryPageSize},
		{"empty", 0, 1, 10, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			offset, limit := historyRange(c.total, c.page, c.pageSize)
			if offset != c.wantOffset || limit != c.wantLimit {
				t.Fatalf("got (%d,%d), want (%d,%d)", offset, limit, c.wantOffset, c.wantLimit)
			}
		})
	}
}

// #B 向不存在的会话发消息必须返回 RecordNotFound，而不是静默创建 AIHelper 并落库孤儿消息
func TestChatSend_RejectsUnknownSession(t *testing.T) {
	withStubSession(t, func(context.Context, string) (*model.Session, error) {
		return nil, nil // 不存在
	})
	_, c := ChatSend("admin", "no-such-session", "你好", "1")
	if c != code.CodeRecordNotFound {
		t.Fatalf("want CodeRecordNotFound for unknown session, got %v", c)
	}
}

// #B 会话不属于当前用户时应拒绝（与历史接口的归属检查一致）
func TestChatSend_RejectsForeignSession(t *testing.T) {
	withStubSession(t, func(_ context.Context, id string) (*model.Session, error) {
		return &model.Session{ID: id, UserName: "someone-else"}, nil
	})
	_, c := ChatSend("admin", "sess-1", "你好", "1")
	if c != code.CodeForbidden {
		t.Fatalf("want CodeForbidden for foreign session, got %v", c)
	}
}

// #B 流式路径同样校验会话存在
func TestStreamMessage_RejectsUnknownSession(t *testing.T) {
	withStubSession(t, func(context.Context, string) (*model.Session, error) {
		return nil, nil
	})
	c := ChatStreamSend("admin", "no-such-session", "你好", "1", discardWriter{})
	if c != code.CodeRecordNotFound {
		t.Fatalf("want CodeRecordNotFound for unknown session, got %v", c)
	}
}

// discardWriter 满足 http.ResponseWriter + Flusher 的空实现，避免流式校验测试触碰网络
type discardWriter struct{}

func (discardWriter) Header() http.Header         { return http.Header{} }
func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriter) WriteHeader(int)             {}
func (discardWriter) Flush()                      {}

// #A hasMore 必须基于截断后的 pageSize 计算：pageSize=1000 被截到 100 时，
// total=250 应该还有更早的消息可翻（旧逻辑用原始 1000 计算会得到 false，更早历史不可达）
func Test_historyHasMore(t *testing.T) {
	cases := []struct {
		name                  string
		total, page, pageSize int
		want                  bool
	}{
		{"oversize pageSize still has more", 250, 1, 1000, true},
		{"exactly one full page", 100, 1, 100, false},
		{"one more than page", 101, 1, 100, true},
		{"middle page", 25, 2, 10, true},
		{"last page", 25, 3, 10, false},
		{"beyond last", 25, 4, 10, false},
		{"no paging returns all", 25, 0, 0, false},
		{"page default", 25, 0, 10, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := historyHasMore(c.total, c.page, c.pageSize); got != c.want {
				t.Fatalf("historyHasMore(%d,%d,%d) = %v, want %v", c.total, c.page, c.pageSize, got, c.want)
			}
		})
	}
}
