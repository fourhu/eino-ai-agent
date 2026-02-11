// Package api provides OpenAI-compatible HTTP API endpoints.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/google/uuid"
	"github.com/hertz-contrib/sse"

	"github.com/fourhu/eino-ai-agent/internal/agent"
	"github.com/fourhu/eino-ai-agent/internal/logger"
)

// OpenAIRequest represents an OpenAI-compatible chat completion request
type OpenAIRequest struct {
	Model    string                 `json:"model"`
	Messages []OpenAIMessage        `json:"messages"`
	Stream   bool                   `json:"stream,omitempty"`
	Session  string                 `json:"session,omitempty"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

// OpenAIMessage represents a message in OpenAI format
type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAIResponse represents an OpenAI-compatible chat completion response
type OpenAIResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice represents a completion choice
type Choice struct {
	Index        int            `json:"index"`
	Message      *OpenAIMessage `json:"message,omitempty"`
	Delta        *OpenAIMessage `json:"delta,omitempty"`
	FinishReason string         `json:"finish_reason"`
}

// Usage represents token usage
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OpenAIStreamEvent represents a server-sent event for streaming
type OpenAIStreamEvent struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
}

// Server handles OpenAI-compatible API requests
type Server struct {
	agent      *agent.Agent
	modelName  string
	httpServer *server.Hertz
}

// NewServer creates a new OpenAI-compatible API server
func NewServer(agent *agent.Agent, modelName string, addr string) *Server {
	h := server.Default(server.WithHostPorts(addr))

	s := &Server{
		agent:      agent,
		modelName:  modelName,
		httpServer: h,
	}

	// Register routes
	h.POST("/v1/chat/completions", s.handleChatCompletions)
	h.GET("/v1/models", s.handleListModels)
	h.GET("/health", s.handleHealth)

	return s
}

// Start starts the HTTP server
func (s *Server) Start() error {
	return s.httpServer.Run()
}

// Stop stops the HTTP server
func (s *Server) Stop(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// handleChatCompletions handles chat completion requests
func (s *Server) handleChatCompletions(ctx context.Context, c *app.RequestContext) {
	var req OpenAIRequest
	if err := c.BindJSON(&req); err != nil {
		logger.Errorf("Failed to parse request: %v", err)
		c.JSON(consts.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("invalid request: %v", err),
		})
		return
	}

	// Generate session ID if not provided
	if req.Session == "" {
		req.Session = uuid.New().String()
	}

	logger.Debugf("[API] Received chat completion request - Session: %s, Model: %s, Stream: %v, Messages: %d",
		req.Session, req.Model, req.Stream, len(req.Messages))

	// Convert messages to a single user message (simplified)
	var userMessage string
	if len(req.Messages) > 0 {
		lastMsg := req.Messages[len(req.Messages)-1]
		if lastMsg.Role == "user" {
			userMessage = lastMsg.Content
		}
	}

	if userMessage == "" {
		logger.Errorf("[API] No user message found in request - Session: %s", req.Session)
		c.JSON(consts.StatusBadRequest, map[string]string{
			"error": "no user message found",
		})
		return
	}

	logger.Debugf("[API] Processing request - Session: %s, UserMessage: %s", req.Session, userMessage)

	if req.Stream {
		s.handleStreamResponse(ctx, c, req.Session, userMessage)
	} else {
		s.handleNonStreamResponse(ctx, c, req.Session, userMessage)
	}
}

// handleNonStreamResponse handles non-streaming responses
func (s *Server) handleNonStreamResponse(ctx context.Context, c *app.RequestContext, sessionID, userMessage string) {
	logger.Debugf("[API] Handling non-stream response - Session: %s", sessionID)

	response, err := s.agent.Chat(ctx, sessionID, userMessage)
	if err != nil {
		logger.Errorf("[API] Chat failed - Session: %s, Error: %v", sessionID, err)
		c.JSON(consts.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("chat failed: %v", err),
		})
		return
	}

	logger.Debugf("[API] Chat completed - Session: %s, ResponseLength: %d", sessionID, len(response.Content))

	resp := OpenAIResponse{
		ID:      fmt.Sprintf("chatcmpl-%s", uuid.New().String()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   s.modelName,
		Choices: []Choice{
			{
				Index: 0,
				Message: &OpenAIMessage{
					Role:    "assistant",
					Content: response.Content,
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     len(userMessage),
			CompletionTokens: len(response.Content),
			TotalTokens:      len(userMessage) + len(response.Content),
		},
	}

	c.JSON(consts.StatusOK, resp)
}

// handleStreamResponse handles streaming responses
func (s *Server) handleStreamResponse(ctx context.Context, c *app.RequestContext, sessionID, userMessage string) {
	logger.Debugf("[API] Handling stream response - Session: %s", sessionID)

	stream, err := s.agent.ChatStream(ctx, sessionID, userMessage)
	if err != nil {
		logger.Errorf("[API] Chat stream failed - Session: %s, Error: %v", sessionID, err)
		c.JSON(consts.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("chat stream failed: %v", err),
		})
		return
	}

	// Set SSE headers
	c.SetContentType("text/event-stream")
	c.Response.Header.Set("Cache-Control", "no-cache")
	c.Response.Header.Set("Connection", "keep-alive")

	sseStream := sse.NewStream(c)

	completionID := fmt.Sprintf("chatcmpl-%s", uuid.New().String())
	created := time.Now().Unix()

	// Send initial role message
	initialEvent := OpenAIStreamEvent{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   s.modelName,
		Choices: []Choice{
			{
				Index: 0,
				Delta: &OpenAIMessage{
					Role: "assistant",
				},
			},
		},
	}
	s.sendSSEEvent(sseStream, initialEvent)

	// Stream content
	var fullContent string
	chunkCount := 0
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			logger.Debugf("[API] Stream ended - Session: %s, TotalChunks: %d", sessionID, chunkCount)
			break
		}
		if err != nil {
			logger.Errorf("[API] Stream error - Session: %s, Error: %v", sessionID, err)
			s.sendSSEEvent(sseStream, OpenAIStreamEvent{
				ID:      completionID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   s.modelName,
				Choices: []Choice{
					{
						Index: 0,
						Delta: &OpenAIMessage{
							Content: fmt.Sprintf("[ERROR: %v]", err),
						},
					},
				},
			})
			break
		}

		if chunk.Content != "" {
			fullContent += chunk.Content
			chunkCount++
			if logger.IsDebugEnabled() && chunkCount%10 == 0 {
				logger.Debugf("[API] Streaming chunk %d - Session: %s", chunkCount, sessionID)
			}
			event := OpenAIStreamEvent{
				ID:      completionID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   s.modelName,
				Choices: []Choice{
					{
						Index: 0,
						Delta: &OpenAIMessage{
							Content: chunk.Content,
						},
					},
				},
			}
			s.sendSSEEvent(sseStream, event)
		}
	}

	logger.Debugf("[API] Stream completed - Session: %s, TotalContentLength: %d", sessionID, len(fullContent))

	// Send finish message
	finishEvent := OpenAIStreamEvent{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   s.modelName,
		Choices: []Choice{
			{
				Index:        0,
				FinishReason: "stop",
			},
		},
	}
	s.sendSSEEvent(sseStream, finishEvent)

	// Update session with full response
	s.agent.AppendAssistantMessage(sessionID, schema.AssistantMessage(fullContent, nil))
}

// sendSSEEvent sends an SSE event
func (s *Server) sendSSEEvent(stream *sse.Stream, event OpenAIStreamEvent) {
	data, _ := json.Marshal(event)
	stream.Publish(&sse.Event{
		Data: data,
	})
}

// handleListModels handles model listing requests
func (s *Server) handleListModels(ctx context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, map[string]interface{}{
		"object": "list",
		"data": []map[string]interface{}{
			{
				"id":       s.modelName,
				"object":   "model",
				"created":  time.Now().Unix(),
				"owned_by": "eino-ai-agent",
			},
		},
	})
}

// handleHealth handles health check requests
func (s *Server) handleHealth(ctx context.Context, c *app.RequestContext) {
	c.JSON(consts.StatusOK, map[string]string{
		"status": "healthy",
	})
}

// RegisterRoutes registers additional custom routes
func (s *Server) RegisterRoutes(register func(h *server.Hertz)) {
	register(s.httpServer)
}
