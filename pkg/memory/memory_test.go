package memory

import (
	"GopherAI/model"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

type fakeStore struct {
	digest   *model.SessionDigest
	msgs     []model.Message
	saveErr  error
	saved    []*model.SessionDigest
	versions []int

	extractCount  int
	extractExists bool
	listCalls     int
}

func (f *fakeStore) GetExtractState(context.Context, string) (int, bool, error) {
	return f.extractCount, f.extractExists, nil
}

func (f *fakeStore) GetDigest(context.Context, string) (*model.SessionDigest, error) {
	return f.digest, nil
}

func (f *fakeStore) SaveDigest(_ context.Context, d *model.SessionDigest, expectVersion int) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, d)
	f.versions = append(f.versions, expectVersion)
	return nil
}

func (f *fakeStore) CountMessages(context.Context, string) (int64, error) {
	return int64(len(f.msgs)), nil
}

func (f *fakeStore) ListMessages(_ context.Context, _ string, offset, limit int) ([]model.Message, error) {
	f.listCalls++
	end := offset + limit
	if end > len(f.msgs) {
		end = len(f.msgs)
	}
	return f.msgs[offset:end], nil
}

type fakeSummarizer struct {
	out   string
	err   error
	input []*schema.Message
}

func (f *fakeSummarizer) Summarize(_ context.Context, msgs []*schema.Message) (string, error) {
	f.input = msgs
	return f.out, f.err
}

func testConfig() Config {
	return Config{
		DigestThreshold: 6,
		KeepRecent:      2,
		WindowHardLimit: 12,
		DigestMaxChars:  50,
		ContextMaxChars: 1000,
	}
}

func makeMsgs(n int) []model.Message {
	msgs := make([]model.Message, n)
	for i := range msgs {
		msgs[i] = model.Message{IsUser: i%2 == 0, Content: fmt.Sprintf("m%d", i)}
	}
	return msgs
}

func fill(m *SessionMemory, n int) {
	for i := 0; i < n; i++ {
		m.Append(Turn{IsUser: i%2 == 0, Content: fmt.Sprintf("m%d", i)})
	}
}

func TestConfig_Normalize(t *testing.T) {
	c := Config{DigestThreshold: 4, KeepRecent: 10, WindowHardLimit: 2}.Normalize()
	if c.KeepRecent >= c.DigestThreshold {
		t.Fatalf("KeepRecent should be < DigestThreshold, got %d/%d", c.KeepRecent, c.DigestThreshold)
	}
	if c.WindowHardLimit < c.DigestThreshold {
		t.Fatalf("WindowHardLimit should be >= DigestThreshold, got %d", c.WindowHardLimit)
	}
	if c.SummarizeTimeout <= 0 || c.DigestMaxChars <= 0 || c.ContextMaxChars <= 0 {
		t.Fatalf("defaults not applied: %+v", c)
	}
}

func TestLoad_WithDigestWatermark(t *testing.T) {
	store := &fakeStore{
		digest: &model.SessionDigest{Content: "旧摘要", DigestedCount: 6, Version: 2},
		msgs:   makeMsgs(9),
	}
	m, err := Load(context.Background(), "s1", testConfig(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := m.Window()
	if len(w) != 3 || w[0].Content != "m6" {
		t.Fatalf("unexpected window: %+v", w)
	}
	if m.Digest() != "旧摘要" {
		t.Fatalf("unexpected digest: %s", m.Digest())
	}
}

func TestLoad_LegacySessionCappedByHardLimit(t *testing.T) {
	store := &fakeStore{msgs: makeMsgs(30)}
	m, err := Load(context.Background(), "s1", testConfig(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := m.Window()
	if len(w) != 12 || w[0].Content != "m18" {
		t.Fatalf("unexpected window: len=%d first=%+v", len(w), w[0])
	}
}

func TestSessionMemory_Compress(t *testing.T) {
	store := &fakeStore{}
	m := New("s1", testConfig(), store)
	fill(m, 7)
	if !m.NeedCompress() {
		t.Fatal("should need compress")
	}

	s := &fakeSummarizer{out: "  新摘要  "}
	if err := m.Compress(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	w := m.Window()
	if len(w) != 2 || w[0].Content != "m5" {
		t.Fatalf("unexpected window after compress: %+v", w)
	}
	if m.Digest() != "新摘要" {
		t.Fatalf("unexpected digest: %q", m.Digest())
	}
	if len(store.saved) != 1 || store.saved[0].DigestedCount != 5 || store.saved[0].Version != 1 || store.versions[0] != 0 {
		t.Fatalf("unexpected saved: %+v versions=%v", store.saved, store.versions)
	}
	if !strings.Contains(s.input[1].Content, "用户：m0") {
		t.Fatalf("summarize input missing turns: %s", s.input[1].Content)
	}

	// 第二次压缩：水位线累加、旧摘要参与整合、乐观锁版本递增
	fill(m, 5)
	s.out = "再次摘要"
	if err := m.Compress(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if store.saved[1].DigestedCount != 10 || store.versions[1] != 1 {
		t.Fatalf("unexpected second save: %+v versions=%v", store.saved[1], store.versions)
	}
	if !strings.Contains(s.input[1].Content, "新摘要") {
		t.Fatal("old digest should be fed into summarizer")
	}
}

func TestSessionMemory_CompressFailureKeepsState(t *testing.T) {
	cases := []struct {
		name  string
		store *fakeStore
		s     *fakeSummarizer
	}{
		{"summarize error", &fakeStore{}, &fakeSummarizer{err: errors.New("llm down")}},
		{"empty digest", &fakeStore{}, &fakeSummarizer{out: "   "}},
		{"save error", &fakeStore{saveErr: errors.New("db down")}, &fakeSummarizer{out: "ok"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New("s1", testConfig(), c.store)
			fill(m, 7)
			if err := m.Compress(context.Background(), c.s); err == nil {
				t.Fatal("expect error")
			}
			if len(m.Window()) != 7 || m.Digest() != "" {
				t.Fatal("state should be unchanged on failure")
			}
		})
	}
}

func TestSessionMemory_BuildContext(t *testing.T) {
	m := New("s1", testConfig(), &fakeStore{})
	m.digest = strings.Repeat("摘", 100)
	fill(m, 3)

	msgs := m.BuildContext()
	if len(msgs) != 4 || msgs[0].Role != schema.System {
		t.Fatalf("unexpected context: %+v", msgs)
	}
	if !strings.Contains(msgs[0].Content, strings.Repeat("摘", 50)) ||
		strings.Contains(msgs[0].Content, strings.Repeat("摘", 51)) {
		t.Fatal("digest should be truncated to DigestMaxChars")
	}
	if msgs[3].Content != "m2" || msgs[3].Role != schema.User {
		t.Fatalf("last message should be current question: %+v", msgs[3])
	}
}

func TestFitBudget(t *testing.T) {
	window := []Turn{
		{IsUser: true, Content: "aaaa"},
		{IsUser: false, Content: "bbbb"},
		{IsUser: true, Content: "cccc"},
	}
	// 预算只够两条：丢弃最旧一条后以助手消息开头，需继续丢弃
	got := fitBudget(window, 8)
	if len(got) != 1 || got[0].Content != "cccc" {
		t.Fatalf("unexpected: %+v", got)
	}
	// 预算不足一条时仍保留本轮问题
	got = fitBudget(window, 1)
	if len(got) != 1 || got[0].Content != "cccc" {
		t.Fatalf("unexpected: %+v", got)
	}
	got = fitBudget(window, 100)
	if len(got) != 3 {
		t.Fatalf("unexpected: %+v", got)
	}
}
