// Package mcp provides MCP (Model Context Protocol) client management functionality.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	mcptool "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/fourhu/eino-ai-agent/internal/logger"
)

// ServerConfig represents a single MCP server configuration
type ServerConfig struct {
	Name    string `json:"name" yaml:"name"`
	BaseURL string `json:"base_url" yaml:"base_url"`
	Enabled bool   `json:"enabled" yaml:"enabled"`
}

// Manager manages multiple MCP clients and tools
type Manager struct {
	configs []ServerConfig
	clients map[string]*client.Client
	tools   []tool.BaseTool
	toolMap map[string]tool.BaseTool // tool name -> tool
	mu      sync.RWMutex
}

// NewManager creates a new MCP manager
func NewManager(configs []ServerConfig) *Manager {
	return &Manager{
		configs: configs,
		clients: make(map[string]*client.Client),
		tools:   make([]tool.BaseTool, 0),
		toolMap: make(map[string]tool.BaseTool),
	}
}

// Initialize initializes connections to all configured MCP servers
func (m *Manager) Initialize(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	logger.Debugf("Initializing %d MCP server configurations", len(m.configs))

	for _, cfg := range m.configs {
		if !cfg.Enabled {
			logger.Debugf("MCP server %s is disabled, skipping", cfg.Name)
			continue
		}
		if cfg.BaseURL == "" {
			logger.Debugf("MCP server %s has no base URL, skipping", cfg.Name)
			continue
		}

		logger.Debugf("Connecting to MCP server: %s at %s", cfg.Name, cfg.BaseURL)
		if err := m.connectServer(ctx, cfg); err != nil {
			logger.Errorf("Failed to connect to MCP server %s: %v", cfg.Name, err)
			return fmt.Errorf("failed to connect to MCP server %s: %w", cfg.Name, err)
		}
		logger.Debugf("Successfully connected to MCP server: %s", cfg.Name)
	}

	logger.Debugf("Total MCP tools loaded: %d", len(m.tools))
	for name := range m.toolMap {
		logger.Debugf("Available tool: %s", name)
	}

	return nil
}

// connectServer connects to a single MCP server
func (m *Manager) connectServer(ctx context.Context, cfg ServerConfig) error {
	logger.Debugf("[MCP:%s] Creating SSE client", cfg.Name)
	cli, err := client.NewSSEMCPClient(cfg.BaseURL)
	if err != nil {
		return fmt.Errorf("failed to create MCP client: %w", err)
	}

	logger.Debugf("[MCP:%s] Starting client", cfg.Name)
	if err := cli.Start(ctx); err != nil {
		return fmt.Errorf("failed to start MCP client: %w", err)
	}

	// Initialize client
	logger.Debugf("[MCP:%s] Initializing client", cfg.Name)
	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    "eino-ai-agent",
		Version: "1.0.0",
	}

	if _, err := cli.Initialize(ctx, initRequest); err != nil {
		return fmt.Errorf("failed to initialize MCP client: %w", err)
	}

	m.clients[cfg.Name] = cli
	logger.Debugf("[MCP:%s] Client initialized successfully", cfg.Name)

	// Get tools from MCP server
	logger.Debugf("[MCP:%s] Fetching tools", cfg.Name)
	tools, err := mcptool.GetTools(ctx, &mcptool.Config{Cli: cli})
	if err != nil {
		return fmt.Errorf("failed to get tools from MCP server: %w", err)
	}

	logger.Debugf("[MCP:%s] Found %d tools", cfg.Name, len(tools))
	for _, t := range tools {
		info, err := t.Info(ctx)
		if err != nil {
			logger.Warnf("[MCP:%s] Failed to get tool info: %v", cfg.Name, err)
			continue
		}

		// Fix schema: ensure properties exists for OpenAI compatibility
		if info.ParamsOneOf != nil {
			if jsonSchema, err := info.ParamsOneOf.ToJSONSchema(); err == nil {
				if jsonSchema.Properties == nil {
					jsonSchema.Properties = jsonschema.NewProperties()
					info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(jsonSchema)
					logger.Debugf("[MCP:%s] Fixed schema for tool: %s (added empty properties)", cfg.Name, info.Name)
				}
			}
		}

		m.toolMap[info.Name] = t
		m.tools = append(m.tools, t)

		if logger.IsDebugEnabled() {
			paramsJSON, _ := json.Marshal(info.ParamsOneOf)
			logger.Debugf("[MCP:%s] Tool loaded: name=%s, desc=%s, params=%s",
				cfg.Name, info.Name, info.Desc, string(paramsJSON))
		}
	}

	return nil
}

// GetTools returns all available tools
func (m *Manager) GetTools() []tool.BaseTool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]tool.BaseTool, len(m.tools))
	copy(result, m.tools)
	return result
}

// GetToolByName gets a specific tool by name
func (m *Manager) GetToolByName(name string) (tool.BaseTool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	t, ok := m.toolMap[name]
	return t, ok
}

// Close closes all MCP client connections
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for name, cli := range m.clients {
		if err := cli.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close client %s: %w", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing MCP clients: %v", errs)
	}
	return nil
}

// GetServerNames returns all connected server names
func (m *Manager) GetServerNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	return names
}
