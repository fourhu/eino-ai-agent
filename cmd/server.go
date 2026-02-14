package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	openaiModel "github.com/cloudwego/eino-ext/components/model/openai"

	"github.com/fourhu/eino-ai-agent/internal/agent"
	"github.com/fourhu/eino-ai-agent/internal/api"
	"github.com/fourhu/eino-ai-agent/internal/config"
	"github.com/fourhu/eino-ai-agent/internal/logger"
	"github.com/fourhu/eino-ai-agent/internal/mcp"
	"github.com/fourhu/eino-ai-agent/internal/memory"
	"github.com/fourhu/eino-ai-agent/internal/summarization"
)

var (
	configFile string
	serverHost string
	serverPort int
	debugMode  bool
)

// serverCmd represents the server command
var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the AI agent server",
	Long: `Start the AI agent server with OpenAI-compatible API endpoints.

The server supports:
- Multi-turn conversations with memory
- MCP (Model Context Protocol) tool integration
- OpenAI-compatible chat completion API
- Streaming and non-streaming responses`,
	RunE: runServer,
}

func init() {
	rootCmd.AddCommand(serverCmd)

	serverCmd.Flags().StringVarP(&configFile, "config", "c", "", "config file path (JSON or YAML format)")
	serverCmd.Flags().StringVar(&serverHost, "host", "", "server host (overrides config)")
	serverCmd.Flags().IntVarP(&serverPort, "port", "p", 0, "server port (overrides config)")
	serverCmd.Flags().BoolVarP(&debugMode, "debug", "d", false, "enable debug logging")

	// Add init-config subcommand
	initConfigCmd := &cobra.Command{
		Use:   "init-config [path]",
		Short: "Generate a default configuration file",
		Long:  `Generate a default configuration file at the specified path (default: config.json).`,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "config.json"
			if len(args) > 0 {
				path = args[0]
			}

			if err := config.GenerateDefaultConfigFile(path); err != nil {
				return err
			}

			fmt.Printf("Generated default configuration file: %s\n", path)
			fmt.Println("Please edit the file and set your API keys and MCP server URLs.")
			return nil
		},
	}
	serverCmd.AddCommand(initConfigCmd)
}

func runServer(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Load configuration
	var cfg *config.Config
	var err error

	if configFile != "" {
		cfg, err = config.LoadFromFile(configFile)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
	} else {
		cfg = config.DefaultConfig()
	}

	// Override with command line flags
	if serverHost != "" {
		cfg.Server.Host = serverHost
	}
	if serverPort != 0 {
		cfg.Server.Port = serverPort
	}
	if debugMode {
		cfg.Log.Level = "debug"
	}

	// Initialize logger
	if err := logger.Init(cfg.Log.Level); err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}

	logger.Infof("Loaded configuration from %s", configFile)
	logger.Infof("Log level: %s", cfg.Log.Level)
	logger.Infof("Memory type: %s", cfg.Memory.Type)

	// Validate model configuration
	if cfg.Model.APIKey == "" {
		return fmt.Errorf("model API key is required (set MODEL_API_KEY env var or config file)")
	}

	// Initialize memory store
	var memStore memory.Store
	var redisStore *memory.RedisStore
	switch cfg.Memory.Type {
	case "redis":
		if cfg.Memory.Address == "" {
			return fmt.Errorf("redis address is required when memory type is 'redis'")
		}
		var err error
		redisStore, err = memory.NewRedisStoreFromAddress(ctx, cfg.Memory.Address, cfg.Memory.Prefix)
		if err != nil {
			return fmt.Errorf("failed to initialize Redis store: %w", err)
		}
		memStore = redisStore
		logger.Infof("Initialized Redis memory store at %s", cfg.Memory.Address)
	case "inmem":
		memStore = memory.NewInMemoryStore()
		logger.Info("Initialized in-memory store")
	default:
		return fmt.Errorf("unsupported memory type: %s", cfg.Memory.Type)
	}
	// Close memory store on shutdown
	defer func() {
		if redisStore != nil {
			if err := redisStore.Close(); err != nil {
				logger.Warnf("Failed to close Redis store: %v", err)
			}
		}
	}()

	// Initialize MCP manager
	mcpManager := mcp.NewManager(cfg.GetEnabledMCPServers())
	if len(cfg.GetEnabledMCPServers()) > 0 {
		logger.Info("Initializing MCP servers...")
		if err := mcpManager.Initialize(ctx); err != nil {
			logger.Warnf("Failed to initialize some MCP servers: %v", err)
		} else {
			logger.Infof("Connected to MCP servers: %v", mcpManager.GetServerNames())
		}
	} else {
		logger.Info("No MCP servers configured")
	}
	defer mcpManager.Close()

	// Create chat model
	chatModel, err := openaiModel.NewChatModel(ctx, &openaiModel.ChatModelConfig{
		BaseURL: cfg.Model.BaseURL,
		APIKey:  cfg.Model.APIKey,
		Model:   cfg.Model.Model,
	})
	if err != nil {
		return fmt.Errorf("failed to create chat model: %w", err)
	}
	logger.Infof("Created chat model: %s", cfg.Model.Model)

	// Create agent
	agentConfig := &agent.Config{
		Model:        chatModel,
		Tools:        mcpManager.GetTools(),
		SystemPrompt: cfg.Agent.SystemPrompt,
		MaxSteps:     cfg.Agent.MaxSteps,
		MemoryStore:  memStore,
	}

	// Add summarization config if enabled
	if cfg.Summarization.Enabled {
		agentConfig.Summarization = &summarization.Config{
			MaxTokensBeforeSummary:     cfg.Summarization.MaxTokensBeforeSummary,
			MaxTokensForRecentMessages: cfg.Summarization.MaxTokensForRecentMessages,
			Model:                      chatModel,
		}
		logger.Info("Conversation summarization enabled")
	}

	aiAgent, err := agent.NewAgent(ctx, agentConfig)
	if err != nil {
		return fmt.Errorf("failed to create agent: %w", err)
	}
	logger.Info("Created ReAct agent")

	// Create and start API server
	apiServer := api.NewServer(aiAgent, cfg.Model.Model, cfg.GetAddress())

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		logger.Info("Shutting down server...")
		apiServer.Stop(ctx)
	}()

	logger.Infof("Starting server on http://%s", cfg.GetAddress())
	logger.Infof("API endpoint: http://%s/v1/chat/completions", cfg.GetAddress())

	if err := apiServer.Start(); err != nil {
		return fmt.Errorf("server error: %w", err)
	}

	return nil
}
