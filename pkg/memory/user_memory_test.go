package memory

import (
	"GopherAI/model"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

type fakeUserStore struct {
	rec     *model.UserMemory
	errs    []error //写给fake的剧本
	onErr   func(*fakeUserStore)
	commits []ExtractionCommit
}

func (f *fakeUserStore) GetUserMemory(context.Context, string) (*model.UserMemory, error) {
	return f.rec, nil
}

func (f *fakeUserStore) CommitExtraction(_ context.Context, c ExtractionCommit) error {
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if f.onErr != nil {
			f.onErr(f)
		}
		return err
	}
	f.commits = append(f.commits, c)
	if c.WriteMemory {
		f.rec = &model.UserMemory{UserName: c.UserName, Entries: c.Entries, Version: c.NewVersion}
	}
	return nil
}

type fakeExtractor struct {
	out   string
	err   error
	input []*schema.Message
}

func (f *fakeExtractor) Extract(_ context.Context, msgs []*schema.Message) (string, error) {
	f.input = msgs
	return f.out, f.err
}

const opAddNoEmoji = `{"ops":[{"op":"add","type":"constraint","text":"不用emoji"}]}`

func l3Config() Config {
	c := testConfig()
	c.ExtractThreshold = 4
	c.ExtractBatch = 10
	c.UserMemoryMaxEntries = 3
	c.UserMemoryEntryMaxChars = 10
	c.UserMemoryMaxChars = 25
	return c.Normalize()
}

func newL3(t *testing.T, us *fakeUserStore, store *fakeStore) (*UserMemory, *SessionMemory) {
	t.Helper()
	ctx := context.Background()
	u, err := LoadUserMemory(ctx, "alice", l3Config(), us)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Load(ctx, "s1", l3Config(), store, u)
	if err != nil {
		t.Fatal(err)
	}
	return u, m
}

func TestApplyOps(t *testing.T) {
	cfg := l3Config()
	ops := []MemoryOp{
		{Op: "add", Type: "constraint", Text: "不用emoji"},
		{Op: "add", Type: "constraint", Text: "不用emoji"},  // 重复，忽略
		{Op: "add", Type: "fact", Text: "这是一条超过十个字的很长记忆"}, // 过长，拒绝
		{Op: "add", Type: "偏好", Text: "先给结论"},
		{Op: "add", Type: "fact", Text: "写Go"},
		{Op: "add", Type: "fact", Text: "第四条"}, // 已满，拒绝且不淘汰旧条目
	}
	out, applied, rejected := applyOps(nil, ops, "s1", cfg)
	if applied != 3 || len(rejected) != 2 || len(out) != 3 {
		t.Fatalf("applied=%d rejected=%v out=%+v", applied, rejected, out)
	}
	if out[0].Text != "不用emoji" || out[1].Type != EntryPreference {
		t.Fatalf("unexpected entries: %+v", out)
	}

	out, applied, rejected = applyOps(out, []MemoryOp{
		{Op: "update", ID: 1, Text: "可以用表情"},
		{Op: "delete", ID: 2},
		{Op: "update", ID: 99, Text: "x"},
	}, "s2", cfg)
	if applied != 2 || len(rejected) != 1 || len(out) != 2 {
		t.Fatalf("applied=%d rejected=%v out=%+v", applied, rejected, out)
	}
	if out[0].Text != "可以用表情" || out[0].Type != EntryConstraint || out[0].Src != "s2" {
		t.Fatalf("update should keep type and change text: %+v", out[0])
	}
	// 删除后新增的 id 不复用
	out, _, _ = applyOps(out, []MemoryOp{{Op: "add", Text: "新的"}}, "s2", cfg)
	if out[len(out)-1].ID != 4 {
		t.Fatalf("expect new id 4, got %+v", out)
	}
}

func TestApplyOps_TotalCharsLimit(t *testing.T) {
	cfg := l3Config()
	base := []MemoryEntry{{ID: 1, Type: EntryFact, Text: strings.Repeat("a", 10)}, {ID: 2, Type: EntryFact, Text: strings.Repeat("b", 10)}}
	out, applied, rejected := applyOps(base, []MemoryOp{{Op: "add", Text: strings.Repeat("c", 6)}}, "s1", cfg)
	if applied != 0 || len(rejected) != 1 || len(out) != 2 {
		t.Fatalf("should reject when total exceeds limit: applied=%d out=%+v", applied, out)
	}
}

func TestParseOps(t *testing.T) {
	ops, err := parseOps("```json\n" + opAddNoEmoji + "\n```")
	if err != nil || len(ops) != 1 || ops[0].Op != "add" {
		t.Fatalf("ops=%+v err=%v", ops, err)
	}
	if ops, err = parseOps(`{"ops":[]}`); err != nil || len(ops) != 0 {
		t.Fatalf("ops=%+v err=%v", ops, err)
	}
	if _, err = parseOps("没有需要记录的内容"); err == nil {
		t.Fatal("expect error for non-json")
	}
}

func TestExtract_CommitAndInjectAsTopRule(t *testing.T) {
	us := &fakeUserStore{}
	u, m := newL3(t, us, &fakeStore{})
	fill(m, 3)
	if m.NeedExtract() {
		t.Fatal("below threshold")
	}
	fill(m, 1)
	if !m.NeedExtract() {
		t.Fatal("should need extract")
	}

	e := &fakeExtractor{out: opAddNoEmoji}
	if err := m.Extract(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.input[1].Content, "用户：m0") {
		t.Fatalf("extract input should contain raw turns: %s", e.input[1].Content)
	}
	c := us.commits[0]
	if !c.WriteMemory || c.ExtractExists || c.ExtractTo != 4 || c.ExpectVersion != 0 || c.NewVersion != 1 {
		t.Fatalf("unexpected commit: %+v", c)
	}
	if m.NeedExtract() || len(u.Entries()) != 1 {
		t.Fatal("watermark should advance and entry should be cached")
	}

	// 长期记忆在 system 最开头，位于摘要之上
	m.digest = "摘要X"
	sys := m.BuildContext()[0]
	ri, di := strings.Index(sys.Content, "不用emoji"), strings.Index(sys.Content, "摘要X")
	if sys.Role != schema.System || ri < 0 || di < 0 || ri > di {
		t.Fatalf("rules should precede digest: %s", sys.Content)
	}

	// 无变更时只推进水位线，不写记忆
	fill(m, 4)
	e.out = `{"ops":[]}`
	if err := m.Extract(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	c = us.commits[1]
	if c.WriteMemory || !c.ExtractExists || c.ExtractExpect != 4 || c.ExtractTo != 8 {
		t.Fatalf("unexpected commit: %+v", c)
	}
}

func TestExtract_FailureKeepsWatermark(t *testing.T) {
	cases := []struct {
		name string
		us   *fakeUserStore
		e    *fakeExtractor
	}{
		{"llm error", &fakeUserStore{}, &fakeExtractor{err: errors.New("llm down")}},
		{"bad json", &fakeUserStore{}, &fakeExtractor{out: "好的"}},
		{"commit error", &fakeUserStore{errs: []error{errors.New("db down")}}, &fakeExtractor{out: opAddNoEmoji}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, m := newL3(t, c.us, &fakeStore{})
			fill(m, 4)
			if err := m.Extract(context.Background(), c.e); err == nil {
				t.Fatal("expect error")
			}
			if !m.NeedExtract() || len(u.Entries()) != 0 || len(c.us.commits) != 0 {
				t.Fatal("watermark and memory should be unchanged on failure")
			}
			// 下一轮重试仍处理同一批消息
			c.e.err, c.e.out = nil, opAddNoEmoji
			if err := m.Extract(context.Background(), c.e); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(c.e.input[1].Content, "用户：m0") || c.us.commits[0].ExtractTo != 4 {
				t.Fatal("retry should cover the same messages")
			}
		})
	}
}

func TestExtract_CrossSessionShared(t *testing.T) {
	ctx := context.Background()
	u, err := LoadUserMemory(ctx, "alice", l3Config(), &fakeUserStore{})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := Load(ctx, "A", l3Config(), &fakeStore{}, u)
	b, _ := Load(ctx, "B", l3Config(), &fakeStore{}, u)
	fill(a, 4)
	fill(b, 1)

	if err := a.Extract(ctx, &fakeExtractor{out: opAddNoEmoji}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.BuildContext()[0].Content, "不用emoji") {
		t.Fatal("session B should see memory extracted in session A")
	}
}

func TestExtract_ReadsDBAfterCompressedOutOfWindow(t *testing.T) {
	store := &fakeStore{
		digest: &model.SessionDigest{Content: "旧摘要", DigestedCount: 6, Version: 1},
		msgs:   makeMsgs(8),
	}
	us := &fakeUserStore{}
	_, m := newL3(t, us, store)
	before := store.listCalls

	e := &fakeExtractor{out: `{"ops":[]}`}
	if err := m.Extract(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if store.listCalls != before+1 || !strings.Contains(e.input[1].Content, "用户：m0") {
		t.Fatal("messages compressed out of window should be read from db")
	}
	if us.commits[0].ExtractTo != 6 {
		t.Fatalf("db batch should stop at window start, got %d", us.commits[0].ExtractTo)
	}
}

// 混合段场景：from < start < to（提取水位线落后于摘要水位线，且一批跨 DB 与窗口）。
// 第一轮只能提取 DB 段 [from, start)，水位线不得越过 start；第二轮从窗口接续，不漏不重。
func TestExtract_MixedSegmentDbThenWindow(t *testing.T) {
	store := &fakeStore{
		digest: &model.SessionDigest{Content: "旧摘要", DigestedCount: 6, Version: 1},
		msgs:   makeMsgs(14),
	}
	us := &fakeUserStore{}
	_, m := newL3(t, us, store)
	// 状态：extracted=0, windowStart=6, window=[m6..m13], total=14
	// ExtractBatch=10 → 初始 to=min(14,10)=10，跨越 start=6

	e := &fakeExtractor{out: `{"ops":[]}`}

	// 第一轮：只提取 DB 段 [0,6)，水位线推进到 6，不得包含窗口内消息
	if err := m.Extract(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	c := us.commits[0]
	if c.ExtractTo != 6 {
		t.Fatalf("first round should stop at window start 6, got %d", c.ExtractTo)
	}
	if strings.Contains(e.input[1].Content, "m6") || !strings.Contains(e.input[1].Content, "m5") {
		t.Fatalf("first round should only contain db turns [0,6): %s", e.input[1].Content)
	}

	// 第二轮：from=6=start，直接取窗口原文 [6,14)，不再回 DB
	listCalls := store.listCalls
	if err := m.Extract(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	c = us.commits[1]
	if c.ExtractExpect != 6 || c.ExtractTo != 14 {
		t.Fatalf("second round should advance 6 -> 14: %+v", c)
	}
	if store.listCalls != listCalls {
		t.Fatal("second round should read from window, not db")
	}
	if !strings.Contains(e.input[1].Content, "m6") || !strings.Contains(e.input[1].Content, "m13") ||
		strings.Contains(e.input[1].Content, "m5") {
		t.Fatalf("second round should contain exactly window turns [6,14): %s", e.input[1].Content)
	}
	if m.NeedExtract() {
		t.Fatal("all messages extracted, should not need extract")
	}
}

func TestUserMemory_ConflictReplay(t *testing.T) {
	us := &fakeUserStore{
		errs: []error{ErrUserMemoryConflict},
		onErr: func(f *fakeUserStore) {
			f.rec = &model.UserMemory{UserName: "alice", Entries: `[{"id":1,"type":"fact","text":"已有"}]`, Version: 3}
		},
	}
	u, m := newL3(t, us, &fakeStore{})
	fill(m, 4)
	if err := m.Extract(context.Background(), &fakeExtractor{out: opAddNoEmoji}); err != nil {
		t.Fatal(err)
	}
	c := us.commits[0]
	if c.ExpectVersion != 3 || c.NewVersion != 4 {
		t.Fatalf("should replay on latest version: %+v", c)
	}
	entries := u.Entries()
	if len(entries) != 2 || entries[0].Text != "已有" || entries[1].ID != 2 {
		t.Fatalf("replay should merge into latest entries: %+v", entries)
	}
}
