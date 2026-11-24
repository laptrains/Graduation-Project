// Command mcp-cli 设备 MCP Server 的命令行客户端，用于联调验证（不经过大模型,自己生成json-schema）。
//
// 用法：
//
//	go run ./cmd/mcp-cli tools    看自动发现的两个工具

// go run ./cmd/mcp-cli list     列出可用设备
// go run ./cmd/mcp-cli control relay_light on      开灯
// go run ./cmd/mcp-cli control servo_curtain set_angle 90   开窗帘
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"GopherAI/common/mcp/client"
)

const (
	defaultURL     = "http://localhost:8081/mcp"
	requestTimeout = 15 * time.Second
	minControlArgs = 3
)

func main() {
	url := flag.String("url", defaultURL, "MCP Server 地址")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: mcp-cli [-url URL] tools | list | control <device_id> <action> [angle]")
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*url, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(url string, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	c, err := client.NewMCPClient(url)
	if err != nil {
		return err
	}
	defer c.Close()
	info, err := c.Initialize(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("connected: %s %s\n", info.ServerInfo.Name, info.ServerInfo.Version)

	switch args[0] {
	case "tools":
		tools, err := c.ListTools(ctx)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(tools, "", "  ")
		fmt.Println(string(b))
		return nil
	case "list":
		return callAndPrint(ctx, c, "list_devices", nil)
	case "control":
		if len(args) < minControlArgs {
			return fmt.Errorf("usage: control <device_id> <action> [angle]")
		}
		params := map[string]any{"device_id": args[1], "action": args[2]}
		if len(args) > minControlArgs {
			angle, err := strconv.Atoi(args[3])
			if err != nil {
				return fmt.Errorf("angle must be integer: %w", err)
			}
			params["angle"] = angle
		}
		return callAndPrint(ctx, c, "control_device", params)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func callAndPrint(ctx context.Context, c *client.MCPClient, tool string, args map[string]any) error {
	start := time.Now()
	res, err := c.CallTool(ctx, tool, args)
	if err != nil {
		return err
	}
	status := "OK"
	if res.IsError {
		status = "TOOL_ERROR"
	}
	fmt.Printf("[%s] %s (%dms)\n%s\n", status, tool, time.Since(start).Milliseconds(), client.ResultText(res))
	return nil
}
