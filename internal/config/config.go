// Package config provides configuration management for the AI agent server.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fourhu/eino-ai-agent/internal/mcp"
	"gopkg.in/yaml.v3"
)

// Config represents the server configuration
type Config struct {
	Server        ServerConfig        `json:"server" yaml:"server"`
	Model         ModelConfig         `json:"model" yaml:"model"`
	MCP           MCPConfig           `json:"mcp" yaml:"mcp"`
	Agent         AgentConfig         `json:"agent" yaml:"agent"`
	Log           LogConfig           `json:"log" yaml:"log"`
	Memory        MemoryConfig        `json:"memory" yaml:"memory"`
	Summarization SummarizationConfig `json:"summarization" yaml:"summarization"`
}

// ServerConfig represents HTTP server configuration
type ServerConfig struct {
	Host string `json:"host" yaml:"host"`
	Port int    `json:"port" yaml:"port"`
}

// ModelConfig represents LLM model configuration
type ModelConfig struct {
	Provider string `json:"provider" yaml:"provider"` // openai, ark, etc.
	BaseURL  string `json:"base_url" yaml:"base_url"`
	APIKey   string `json:"api_key" yaml:"api_key"`
	Model    string `json:"model" yaml:"model"`
}

// MCPConfig represents MCP server configurations
type MCPConfig struct {
	Servers []mcp.ServerConfig `json:"servers" yaml:"servers"`
}

// MemoryConfig represents memory storage configuration
type MemoryConfig struct {
	Type    string `json:"type" yaml:"type"`       // "inmem" or "redis"
	Address string `json:"address" yaml:"address"` // Redis address (e.g., "localhost:6379")
	Prefix  string `json:"prefix" yaml:"prefix"`   // Key prefix for Redis
}

// AgentConfig represents agent behavior configuration
type AgentConfig struct {
	SystemPrompt string `json:"system_prompt" yaml:"system_prompt"`
	MaxSteps     int    `json:"max_steps" yaml:"max_steps"`
	MaxHistory   int    `json:"max_history" yaml:"max_history"` // Max conversation rounds to keep (0 = unlimited)
}

// SummarizationConfig represents conversation summarization configuration
type SummarizationConfig struct {
	Enabled                    bool `json:"enabled" yaml:"enabled"`
	MaxTokensBeforeSummary     int  `json:"max_tokens_before_summary" yaml:"max_tokens_before_summary"`           // Token threshold to trigger summarization
	MaxTokensForRecentMessages int  `json:"max_tokens_for_recent_messages" yaml:"max_tokens_for_recent_messages"` // Token budget for recent messages after summarization
}

// LogConfig represents logging configuration
type LogConfig struct {
	Level string `json:"level" yaml:"level"` // debug, info, warn, error
}

// DefaultConfig returns a default configuration with environment variable overrides
func DefaultConfig() *Config {
	cfg := &Config{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8080,
		},
		Model: ModelConfig{
			Provider: "openai",
			BaseURL:  "https://api.openai.com/v1",
			Model:    "gpt-4o",
		},
		MCP: MCPConfig{
			Servers: []mcp.ServerConfig{},
		},
		Agent: AgentConfig{
			SystemPrompt: "You are a helpful AI assistant with access to various tools through MCP servers.",
			MaxSteps:     20,
		},
		Log: LogConfig{
			Level: "info",
		},
		Memory: MemoryConfig{
			Type:   "inmem",
			Prefix: "eino:session:",
		},
	}
	cfg.loadFromEnv()
	return cfg
}

// LoadFromFile loads configuration from a JSON or YAML file
func LoadFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	config := DefaultConfig()

	// Detect format based on file extension
	ext := strings.ToLower(filepath.Ext(path))

	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, config); err != nil {
			return nil, fmt.Errorf("failed to parse YAML config file: %w", err)
		}
	case ".json", "":
		if err := json.Unmarshal(data, config); err != nil {
			return nil, fmt.Errorf("failed to parse JSON config file: %w", err)
		}
	default:
		// Try JSON first, then YAML
		if err := json.Unmarshal(data, config); err != nil {
			if err := yaml.Unmarshal(data, config); err != nil {
				return nil, fmt.Errorf("failed to parse config file (tried JSON and YAML): %w", err)
			}
		}
	}

	// Override with environment variables
	config.loadFromEnv()

	return config, nil
}

// loadFromEnv overrides configuration with environment variables
func (c *Config) loadFromEnv() {
	if host := os.Getenv("SERVER_HOST"); host != "" {
		c.Server.Host = host
	}
	if port := os.Getenv("SERVER_PORT"); port != "" {
		if p, err := parseInt(port); err == nil {
			c.Server.Port = p
		}
	}
	if baseURL := os.Getenv("MODEL_BASE_URL"); baseURL != "" {
		c.Model.BaseURL = baseURL
	}
	if apiKey := os.Getenv("MODEL_API_KEY"); apiKey != "" {
		c.Model.APIKey = apiKey
	}
	if model := os.Getenv("MODEL_NAME"); model != "" {
		c.Model.Model = model
	}
	if provider := os.Getenv("MODEL_PROVIDER"); provider != "" {
		c.Model.Provider = provider
	}
	if systemPrompt := os.Getenv("AGENT_SYSTEM_PROMPT"); systemPrompt != "" {
		c.Agent.SystemPrompt = systemPrompt
	}
	if logLevel := os.Getenv("LOG_LEVEL"); logLevel != "" {
		c.Log.Level = logLevel
	}
	if memoryType := os.Getenv("MEMORY_TYPE"); memoryType != "" {
		c.Memory.Type = memoryType
	}
	if memoryAddr := os.Getenv("MEMORY_ADDRESS"); memoryAddr != "" {
		c.Memory.Address = memoryAddr
	}
}

// GetAddress returns the server address in host:port format
func (c *Config) GetAddress() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}

// GetEnabledMCPServers returns only enabled MCP server configs
func (c *Config) GetEnabledMCPServers() []mcp.ServerConfig {
	var enabled []mcp.ServerConfig
	for _, s := range c.MCP.Servers {
		if s.Enabled && s.BaseURL != "" {
			enabled = append(enabled, s)
		}
	}
	return enabled
}

func parseInt(s string) (int, error) {
	var result int
	_, err := fmt.Sscanf(s, "%d", &result)
	return result, err
}

// GenerateDefaultConfigFile generates a default configuration file at the specified path
// Supports both JSON and YAML formats based on file extension
func GenerateDefaultConfigFile(path string) error {
	cfg := &Config{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8080,
		},
		Model: ModelConfig{
			Provider: "openai",
			BaseURL:  "https://api.openai.com/v1",
			APIKey:   "${MODEL_API_KEY}",
			Model:    "gpt-4o",
		},
		MCP: MCPConfig{
			Servers: []mcp.ServerConfig{
				{
					Name:    "kubernetes-mcp-server",
					BaseURL: "http://localhost:3001/sse",
					Enabled: false,
				},
				{
					Name:    "azure-mcp-server",
					BaseURL: "http://localhost:3002/sse",
					Enabled: false,
				},
			},
		},
		Agent: AgentConfig{
			SystemPrompt: "You are a helpful AI assistant with access to various tools through MCP servers.",
			MaxSteps:     20,
		},
		Log: LogConfig{
			Level: "info",
		},
		Memory: MemoryConfig{
			Type:   "inmem",
			Prefix: "eino:session:",
		},
	}

	ext := strings.ToLower(filepath.Ext(path))

	var data []byte
	var err error

	switch ext {
	case ".yaml", ".yml":
		data, err = yaml.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("failed to marshal config to YAML: %w", err)
		}
	default:
		// Default to JSON
		data, err = json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal config to JSON: %w", err)
		}
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}
