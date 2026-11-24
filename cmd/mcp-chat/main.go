// Command mcp-chat 通过 MCPModel 走完整链路（LLM → tools/list → 多轮 Tool Calling → MCP Server → 模拟器），
// 用于阶段 3 的端到端验收与演示。
//
// 用法（需先 source .env，并确保 EMQX、simulator、device-mcp-server 已启动）：
//
//	go run ./cmd/mcp-chat "把灯打开，窗帘拉开一半"
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"GopherAI/common/aihelper"

	"github.com/cloudwego/eino/schema"
)

const requestTimeout = 60 * time.Second

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mcp-chat <用户指令>")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	m, err := aihelper.NewMCPModel(ctx, "admin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create model:", err)
		os.Exit(1)
	}
	defer m.Close()

	question := os.Args[1]
	fmt.Printf(">>> %s\n", question)

	start := time.Now()
	resp, err := m.GenerateResponse(ctx, []*schema.Message{schema.UserMessage(question)})
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate failed:", err)
		os.Exit(1)
	}
	fmt.Printf("<<< %s\n(总耗时 %dms)\n", resp.Content, time.Since(start).Milliseconds())
}
