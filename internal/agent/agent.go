// Package agent provides ChatModel agent implementation with memory support using Eino ADK.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/fourhu/eino-ai-agent/internal/logger"
	"github.com/fourhu/eino-ai-agent/internal/memory"
)

// Config is the agent configuration
type Config struct {
	Model        model.ToolCallingChatModel
	Tools        []tool.BaseTool
	SystemPrompt string
	MaxSteps     int
	MaxHistory   int // Max conversation rounds to keep (0 = unlimited)
	MemoryStore  memory.Store
}

// Session represents a conversation session
type Session struct {
	ID       string
	Messages []*schema.Message
	mu       sync.RWMutex
}

// Agent is a multi-turn conversation ChatModel agent using ADK
type Agent struct {
	config      *Config
	runner      *adk.Runner
	sessions    map[string]*Session
	sessionMu   sync.RWMutex
	memoryStore memory.Store
}

// NewAgent creates a new ADK ChatModel agent with Runner
func NewAgent(ctx context.Context, config *Config) (*Agent, error) {
	if config.MaxSteps == 0 {
		config.MaxSteps = 20 // Default max iterations
	}

	// Create middleware for history truncation and tool result formatting
	middlewares := []adk.AgentMiddleware{}
	if config.MaxHistory > 0 {
		middlewares = append(middlewares, adk.AgentMiddleware{
			BeforeChatModel: func(ctx context.Context, state *adk.ChatModelAgentState) error {
				if len(state.Messages) > config.MaxHistory*2 {
					// Keep only the last MaxHistory rounds (user + assistant = 2 messages per round)
					state.Messages = state.Messages[len(state.Messages)-config.MaxHistory*2:]
					logger.Debugf("Applied history limit: keeping last %d messages (max %d rounds)",
						len(state.Messages), config.MaxHistory)
				}
				return nil
			},
		})
	}
	// Add middleware to format tool results
	middlewares = append(middlewares, adk.AgentMiddleware{
		AfterChatModel: func(ctx context.Context, state *adk.ChatModelAgentState) error {
			// Format tool results in messages
			for i, msg := range state.Messages {
				if msg.Role == schema.Tool && msg.Content != "" {
					formatted := formatToolResult(msg.Content)
					if formatted != msg.Content {
						// Preserve the original ToolCallID
						toolCallID := ""
						if len(msg.ToolCalls) > 0 {
							toolCallID = msg.ToolCalls[0].ID
						}
						state.Messages[i] = schema.ToolMessage(formatted, toolCallID)
						logger.Debugf("Formatted tool result")
					}
				}
			}
			return nil
		},
	})

	// Create ADK ChatModel agent
	chatModelAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "eino-ai-agent",
		Description: "A helpful AI assistant with access to various tools through MCP servers",
		Instruction: config.SystemPrompt,
		Model:       config.Model,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: config.Tools,
			},
		},
		MaxIterations: config.MaxSteps,
		Middlewares:   middlewares,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create chat model agent: %w", err)
	}

	// Create ADK Runner with streaming enabled
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		EnableStreaming: true,
		Agent:           chatModelAgent,
		CheckPointStore: &checkpointStore{memoryStore: config.MemoryStore},
	})

	// Use in-memory store if no memory store provided
	store := config.MemoryStore
	if store == nil {
		store = memory.NewInMemoryStore()
		logger.Debug("Using in-memory session store")
	}

	return &Agent{
		config:      config,
		runner:      runner,
		sessions:    make(map[string]*Session),
		memoryStore: store,
	}, nil
}

// GetOrCreateSession gets or creates a session
func (a *Agent) GetOrCreateSession(ctx context.Context, sessionID string) *Session {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	if session, exists := a.sessions[sessionID]; exists {
		return session
	}

	// Try to load from persistent storage
	var msgs []*schema.Message
	if a.memoryStore != nil {
		var err error
		msgs, err = a.memoryStore.Read(ctx, sessionID)
		if err != nil {
			logger.Warnf("Failed to read session %s from memory store: %v", sessionID, err)
		}
		if msgs != nil {
			logger.Debugf("Loaded session %s from memory store (%d messages)", sessionID, len(msgs))
		}
	}

	if msgs == nil {
		msgs = make([]*schema.Message, 0)
	}

	session := &Session{
		ID:       sessionID,
		Messages: msgs,
	}
	a.sessions[sessionID] = session
	return session
}

// persistSession saves session messages to memory store
func (a *Agent) persistSession(ctx context.Context, sessionID string, msgs []*schema.Message) {
	if a.memoryStore == nil {
		return
	}

	if err := a.memoryStore.Write(ctx, sessionID, msgs); err != nil {
		logger.Warnf("Failed to persist session %s: %v", sessionID, err)
	} else {
		logger.Debugf("Persisted session %s (%d messages)", sessionID, len(msgs))
	}
}

// Chat performs multi-turn conversation
func (a *Agent) Chat(ctx context.Context, sessionID string, userMessage string) (*schema.Message, error) {
	session := a.GetOrCreateSession(ctx, sessionID)

	session.mu.Lock()
	defer session.mu.Unlock()

	// Add user message to history
	session.Messages = append(session.Messages, schema.UserMessage(userMessage))

	logger.Debugf("[Session: %s] User message: %s", sessionID, userMessage)
	logger.Debugf("[Session: %s] Conversation history length: %d", sessionID, len(session.Messages))

	// Use Runner to query with checkpoint
	events := a.runner.Query(ctx, userMessage, adk.WithCheckPointID(sessionID))

	// Collect response from events
	var response *schema.Message
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			logger.Errorf("[Session: %s] Event error: %v", sessionID, event.Err)
			continue
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			msg, err := event.Output.MessageOutput.GetMessage()
			if err == nil && msg != nil {
				response = msg
			}
		}
	}

	if response == nil {
		return nil, fmt.Errorf("no assistant response received")
	}

	logger.Debugf("[Session: %s] Agent response - Role: %s, Content: %s", sessionID, response.Role, response.Content)

	// Add assistant response to history
	session.Messages = append(session.Messages, response)

	// Persist to memory store
	a.persistSession(ctx, sessionID, session.Messages)

	return response, nil
}

// ChatStream performs streaming multi-turn conversation
func (a *Agent) ChatStream(ctx context.Context, sessionID string, userMessage string) (*schema.StreamReader[*schema.Message], error) {
	session := a.GetOrCreateSession(ctx, sessionID)

	session.mu.Lock()
	defer session.mu.Unlock()

	// Add user message to history
	session.Messages = append(session.Messages, schema.UserMessage(userMessage))

	logger.Debugf("[Session: %s] User message (streaming): %s", sessionID, userMessage)
	logger.Debugf("[Session: %s] Conversation history length: %d", sessionID, len(session.Messages))

	// Persist user message immediately for streaming
	a.persistSession(ctx, sessionID, session.Messages)

	// Use Runner to query with streaming
	events := a.runner.Query(ctx, userMessage, adk.WithCheckPointID(sessionID))

	// Create stream reader with larger buffer
	streamReader, streamWriter := schema.Pipe[*schema.Message](100)

	// Use WaitGroup to ensure goroutine starts before returning
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		wg.Done()
		defer streamWriter.Close()
		for {
			event, ok := events.Next()
			if !ok {
				logger.Debugf("[Session: %s] Event stream completed", sessionID)
				break
			}
			if event.Err != nil {
				logger.Errorf("[Session: %s] Event error: %v", sessionID, event.Err)
				continue
			}

			if event.Output != nil && event.Output.MessageOutput != nil {
				if event.Output.MessageOutput.IsStreaming && event.Output.MessageOutput.MessageStream != nil {
					// Handle streaming message
					for {
						chunk, err := event.Output.MessageOutput.MessageStream.Recv()
						if err != nil {
							break
						}
						if chunk == nil {
							continue
						}
						// Send chunk to stream - even if Send returns false, continue reading from MessageStream
						// to ensure the MessageStream is fully consumed
						streamWriter.Send(chunk, nil)
					}
				} else if event.Output.MessageOutput.Message != nil {
					// Handle non-streaming message
					streamWriter.Send(event.Output.MessageOutput.Message, nil)
				}
			}
		}
	}()

	// Wait for goroutine to start
	wg.Wait()

	return streamReader, nil
}

// GetSessionHistory gets session message history
func (a *Agent) GetSessionHistory(sessionID string) ([]*schema.Message, bool) {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()

	session, exists := a.sessions[sessionID]
	if !exists {
		return nil, false
	}

	session.mu.RLock()
	defer session.mu.RUnlock()

	result := make([]*schema.Message, len(session.Messages))
	copy(result, session.Messages)
	return result, true
}

// ClearSession clears session history
func (a *Agent) ClearSession(sessionID string) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	delete(a.sessions, sessionID)
}

// ListSessions lists all session IDs
func (a *Agent) ListSessions() []string {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()

	sessionIDs := make([]string, 0, len(a.sessions))
	for id := range a.sessions {
		sessionIDs = append(sessionIDs, id)
	}
	return sessionIDs
}

// AppendAssistantMessage appends assistant message to session (used after streaming response)
func (a *Agent) AppendAssistantMessage(sessionID string, message *schema.Message) {
	a.sessionMu.RLock()
	session, exists := a.sessions[sessionID]
	a.sessionMu.RUnlock()

	if !exists {
		return
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	session.Messages = append(session.Messages, message)
}

// checkpointStore implements adk.CheckPointStore interface
type checkpointStore struct {
	memoryStore memory.Store
}

func (c *checkpointStore) Get(ctx context.Context, checkPointID string) ([]byte, bool, error) {
	if c.memoryStore == nil {
		return nil, false, fmt.Errorf("memory store not available")
	}
	// TODO: Implement checkpoint serialization
	return nil, false, nil
}

func (c *checkpointStore) Set(ctx context.Context, checkPointID string, checkPoint []byte) error {
	if c.memoryStore == nil {
		return fmt.Errorf("memory store not available")
	}
	// TODO: Implement checkpoint serialization
	return nil
}

// formatToolResult formats MCP tool result JSON into human-readable format
func formatToolResult(content string) string {
	// Check if it's MCP tool result format
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, `{"content"`) {
		return content
	}

	// Parse MCP tool result
	var mcpResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}

	if err := json.Unmarshal([]byte(content), &mcpResult); err != nil {
		return content
	}

	// Extract text content
	var result strings.Builder
	for _, item := range mcpResult.Content {
		if item.Type == "text" && item.Text != "" {
			result.WriteString(item.Text)
		}
	}

	formatted := result.String()
	if formatted == "" {
		return content
	}
	return formatted
}
