// Package agent provides ReAct agent implementation with memory support.
package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
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
	MaxHistory   int
	MemoryStore  memory.Store
}

// Session represents a conversation session
type Session struct {
	ID       string
	Messages []*schema.Message
	mu       sync.RWMutex
}

// Agent is a multi-turn conversation ReAct agent
type Agent struct {
	config      *Config
	agent       *react.Agent
	sessions    map[string]*Session
	sessionMu   sync.RWMutex
	memoryStore memory.Store
}

// NewAgent creates a new ReAct agent
func NewAgent(ctx context.Context, config *Config) (*Agent, error) {
	if config.MaxSteps == 0 {
		config.MaxSteps = 20
	}

	// Create ReAct agent
	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: config.Model,
		ToolsConfig:      compose.ToolsNodeConfig{Tools: config.Tools},
		MaxStep:          config.MaxSteps,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create react agent: %w", err)
	}

	// Use in-memory store if no memory store provided
	store := config.MemoryStore
	if store == nil {
		store = memory.NewInMemoryStore()
		logger.Debug("Using in-memory session store")
	}

	return &Agent{
		config:      config,
		agent:       agent,
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
	}
}

// Chat performs multi-turn conversation
func (a *Agent) Chat(ctx context.Context, sessionID string, userMessage string) (*schema.Message, error) {
	session := a.GetOrCreateSession(ctx, sessionID)

	session.mu.Lock()
	defer session.mu.Unlock()

	// Add system prompt if first message
	if len(session.Messages) == 0 && a.config.SystemPrompt != "" {
		session.Messages = append(session.Messages, schema.SystemMessage(a.config.SystemPrompt))
	}

	// Add user message
	session.Messages = append(session.Messages, schema.UserMessage(userMessage))

	// Apply history limit
	if a.config.MaxHistory > 0 && len(session.Messages) > a.config.MaxHistory*2+1 {
		session.Messages = append([]*schema.Message{session.Messages[0]}, session.Messages[len(session.Messages)-a.config.MaxHistory*2:]...)
	}

	// Run agent
	response, err := a.agent.Generate(ctx, session.Messages)
	if err != nil {
		return nil, fmt.Errorf("agent generate failed: %w", err)
	}

	// Add assistant response to history
	session.Messages = append(session.Messages, response)
	a.persistSession(ctx, sessionID, session.Messages)

	return response, nil
}

// ChatStream performs streaming multi-turn conversation
func (a *Agent) ChatStream(ctx context.Context, sessionID string, userMessage string) (*schema.StreamReader[*schema.Message], error) {
	session := a.GetOrCreateSession(ctx, sessionID)

	session.mu.Lock()
	defer session.mu.Unlock()

	// Add system prompt if first message
	if len(session.Messages) == 0 && a.config.SystemPrompt != "" {
		session.Messages = append(session.Messages, schema.SystemMessage(a.config.SystemPrompt))
	}

	// Add user message
	session.Messages = append(session.Messages, schema.UserMessage(userMessage))

	// Apply history limit
	if a.config.MaxHistory > 0 && len(session.Messages) > a.config.MaxHistory*2+1 {
		session.Messages = append([]*schema.Message{session.Messages[0]}, session.Messages[len(session.Messages)-a.config.MaxHistory*2:]...)
	}

	// Persist user message
	a.persistSession(ctx, sessionID, session.Messages)

	// Stream response
	streamReader, err := a.agent.Stream(ctx, session.Messages)
	if err != nil {
		return nil, fmt.Errorf("agent stream failed: %w", err)
	}

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

// AppendAssistantMessage appends assistant message to session
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
