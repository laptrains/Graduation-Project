// Package client 通用 MCP 客户端封装（Streamable HTTP），供 aihelper 与验证工具使用。
package client

import (
	"context"
	"fmt"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// 客户端标识
const (
	clientName    = "GopherAI MCP Client"
	clientVersion = "1.0.0"
)

// MCPClient MCP 客户端
type MCPClient struct {
	c *mcpclient.Client
}

// NewMCPClient 创建基于 Streamable HTTP 的客户端，例如 http://localhost:8081/mcp
func NewMCPClient(httpURL string) (*MCPClient, error) {
	tr, err := transport.NewStreamableHTTP(httpURL)
	if err != nil {
		return nil, fmt.Errorf("create transport: %w", err)
	}
	return &MCPClient{c: mcpclient.NewClient(tr)}, nil
}

// NewFromClient 包装已有的 mcp-go 客户端（如进程内客户端，测试用）
func NewFromClient(c *mcpclient.Client) *MCPClient {
	return &MCPClient{c: c}
}

// Initialize 启动传输并完成 MCP 握手
func (m *MCPClient) Initialize(ctx context.Context) (*mcp.InitializeResult, error) {
	if err := m.c.Start(ctx); err != nil {
		return nil, fmt.Errorf("start transport: %w", err)
	}
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: clientName, Version: clientVersion}
	req.Params.Capabilities = mcp.ClientCapabilities{}
	res, err := m.c.Initialize(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	return res, nil
}

// Ping 健康检查
func (m *MCPClient) Ping(ctx context.Context) error {
	if err := m.c.Ping(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	return nil
}

// ListTools 获取服务端全部工具（自动翻页）
func (m *MCPClient) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	res, err := m.c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	return res.Tools, nil
}

// CallTool 调用工具；业务失败通过 result.IsError 表达，error 仅表示协议/传输错误
func (m *MCPClient) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := m.c.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("call tool %s: %w", name, err)
	}
	return res, nil
}

// ResultText 拼接工具结果中的文本内容
func ResultText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	parts := make([]string, 0, len(res.Content))
	for _, c := range res.Content {
		if t, ok := c.(mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// Close 关闭连接
func (m *MCPClient) Close() error {
	if m.c == nil {
		return nil
	}
	return m.c.Close()
}
