#!/bin/bash

# MCP Tools Inspector Script
# 用于获取 MCP 服务器的工具列表和 JSON Schema 描述

set -e

# 默认配置
MCP_SERVER_URL="${MCP_SERVER_URL:-http://localhost:8080/sse}"
OUTPUT_DIR="${OUTPUT_DIR:-./mcp_tools_output}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 显示用法
usage() {
    cat << EOF
Usage: $0 [OPTIONS]

MCP Tools Inspector - 获取 MCP 服务器的工具列表和 JSON Schema

Options:
    -u, --url URL       MCP 服务器 URL (默认: http://localhost:8080/sse)
    -o, --output DIR    输出目录 (默认: ./mcp_tools_output)
    -h, --help          显示此帮助信息

Environment Variables:
    MCP_SERVER_URL      MCP 服务器 URL
    OUTPUT_DIR          输出目录

Examples:
    $0                                    # 使用默认配置
    $0 -u http://localhost:3001/sse       # 指定 MCP 服务器
    $0 -u http://localhost:8080/sse -o ./tools  # 指定输出目录

EOF
}

# 解析参数
while [[ $# -gt 0 ]]; do
    case $1 in
        -u|--url)
            MCP_SERVER_URL="$2"
            shift 2
            ;;
        -o|--output)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            log_error "未知参数: $1"
            usage
            exit 1
            ;;
    esac
done

log_info "MCP Server URL: $MCP_SERVER_URL"
log_info "Output Directory: $OUTPUT_DIR"

# 获取绝对路径
OUTPUT_DIR="$(cd "$(dirname "$OUTPUT_DIR")" && pwd)/$(basename "$OUTPUT_DIR")"

# 创建输出目录
mkdir -p "$OUTPUT_DIR"

# 创建临时目录
TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

# 创建 Go 程序来获取工具信息
cat > "$TEMP_DIR/get_tools.go" << 'GOEOF'
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <mcp-server-url>\n", os.Args[0])
		os.Exit(1)
	}

	serverURL := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 创建 SSE 客户端
	cli, err := client.NewSSEMCPClient(serverURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create MCP client: %v\n", err)
		os.Exit(1)
	}

	if err := cli.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start MCP client: %v\n", err)
		os.Exit(1)
	}
	defer cli.Close()

	// 初始化客户端
	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    "mcp-tools-inspector",
		Version: "1.0.0",
	}

	if _, err := cli.Initialize(ctx, initRequest); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize MCP client: %v\n", err)
		os.Exit(1)
	}

	// 获取工具列表
	toolsRequest := mcp.ListToolsRequest{}
	toolsResult, err := cli.ListTools(ctx, toolsRequest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list tools: %v\n", err)
		os.Exit(1)
	}

	// 输出工具信息
	output := struct {
		ServerURL string        `json:"server_url"`
		ToolCount int           `json:"tool_count"`
		Tools     []mcp.Tool    `json:"tools"`
	}{
		ServerURL: serverURL,
		ToolCount: len(toolsResult.Tools),
		Tools:     toolsResult.Tools,
	}

	jsonOutput, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal output: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(string(jsonOutput))
}
GOEOF

# 初始化 Go 模块
cd "$TEMP_DIR"
go mod init mcp-tools-inspector 2>/dev/null || true

# 添加依赖
go get github.com/mark3labs/mcp-go@v0.43.2 2>/dev/null || true

log_info "正在获取 MCP 工具列表..."

# 运行程序获取工具信息
if ! go run get_tools.go "$MCP_SERVER_URL" > "$OUTPUT_DIR/tools.json" 2>"$OUTPUT_DIR/error.log"; then
    log_error "获取工具列表失败"
    if [ -f "$OUTPUT_DIR/error.log" ]; then
        cat "$OUTPUT_DIR/error.log"
    fi
    exit 1
fi

# 删除错误日志
rm -f "$OUTPUT_DIR/error.log"

log_success "工具列表已保存到: $OUTPUT_DIR/tools.json"

# 解析并显示工具摘要
echo ""
echo "========================================"
echo "MCP Tools Summary"
echo "========================================"

# 使用 Python 或 jq 来解析 JSON（如果可用）
if command -v python3 &> /dev/null; then
    python3 << PYEOF
import json
import sys

try:
    with open("$OUTPUT_DIR/tools.json", "r") as f:
        data = json.load(f)
    
    print(f"Server URL: {data.get('server_url', 'N/A')}")
    print(f"Total Tools: {data.get('tool_count', 0)}")
    print("")
    
    for i, tool in enumerate(data.get('tools', []), 1):
        name = tool.get('name', 'N/A')
        desc = tool.get('description', 'No description')
        print(f"{i}. {name}")
        print(f"   Description: {desc}")
        
        # 检查 inputSchema
        schema = tool.get('inputSchema', {})
        schema_type = schema.get('type', 'N/A')
        properties = schema.get('properties', {})
        required = schema.get('required', [])
        
        print(f"   Schema Type: {schema_type}")
        print(f"   Properties Count: {len(properties)}")
        if required:
            print(f"   Required Fields: {', '.join(required)}")
        
        # 保存单个工具的 schema
        tool_file = f"$OUTPUT_DIR/tool_{name}_schema.json"
        with open(tool_file, "w") as f:
            json.dump(schema, f, indent=2)
        print(f"   Schema File: {tool_file}")
        print("")
        
except Exception as e:
    print(f"Error parsing JSON: {e}", file=sys.stderr)
    sys.exit(1)
PYEOF
elif command -v jq &> /dev/null; then
    echo "Server URL: $(jq -r '.server_url' "$OUTPUT_DIR/tools.json")"
    echo "Total Tools: $(jq '.tool_count' "$OUTPUT_DIR/tools.json")"
    echo ""
    jq -r '.tools[] | "\nName: \(.name)\nDescription: \(.description)\nSchema Type: \(.inputSchema.type // "N/A")\nProperties: \(.inputSchema.properties | keys | join(", ") // "None")"' "$OUTPUT_DIR/tools.json"
else
    log_warn "未找到 python3 或 jq，只输出原始 JSON"
    cat "$OUTPUT_DIR/tools.json"
fi

echo "========================================"
echo ""
log_success "所有工具 schema 已保存到: $OUTPUT_DIR/"
log_info "你可以使用以下命令查看详细信息:"
echo "  cat $OUTPUT_DIR/tools.json | jq ."
echo "  cat $OUTPUT_DIR/tool_<name>_schema.json | jq ."
