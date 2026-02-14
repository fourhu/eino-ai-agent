// Package summarization provides conversation history summarization middleware.
// It compacts long conversation history into a single summary message when token threshold is exceeded.
package summarization

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// TokenCounter counts tokens for messages
type TokenCounter func(ctx context.Context, msgs []*schema.Message) ([]int64, error)

// Config defines parameters for the conversation summarization middleware
type Config struct {
	// MaxTokensBeforeSummary is the max token threshold to trigger summarization
	// Default: 2000
	MaxTokensBeforeSummary int

	// MaxTokensForRecentMessages is the max token budget reserved for recent messages after summarization
	// Default: 500
	MaxTokensForRecentMessages int

	// Counter custom token counter. If nil, uses default counter
	Counter TokenCounter

	// Model used to generate the summary. Required.
	Model model.ToolCallingChatModel

	// SystemPrompt is the system prompt for the summarizer
	// Optional. If empty, uses default prompt.
	SystemPrompt string
}

const (
	// DefaultMaxTokensBeforeSummary default token threshold to trigger summarization
	DefaultMaxTokensBeforeSummary = 2000
	// DefaultMaxTokensForRecentMessages default token budget for recent messages
	DefaultMaxTokensForRecentMessages = 500
)

const summaryMessageFlag = "_summary_message"

// Middleware provides conversation summarization functionality
type Middleware struct {
	counter   TokenCounter
	maxBefore int
	maxRecent int

	summarizer compose.Runnable[map[string]any, *schema.Message]
}

// New creates a new summarization middleware
func New(ctx context.Context, cfg *Config) (*Middleware, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	if cfg.Model == nil {
		return nil, fmt.Errorf("model is required")
	}

	systemPrompt := cfg.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = defaultSummaryPrompt
	}

	maxBefore := DefaultMaxTokensBeforeSummary
	if cfg.MaxTokensBeforeSummary > 0 {
		maxBefore = cfg.MaxTokensBeforeSummary
	}

	maxRecent := DefaultMaxTokensForRecentMessages
	if cfg.MaxTokensForRecentMessages > 0 {
		maxRecent = cfg.MaxTokensForRecentMessages
	}

	tpl := prompt.FromMessages(schema.FString,
		schema.SystemMessage(systemPrompt),
		schema.UserMessage("Please summarize the following conversation history:\n\n{older_messages}"))

	summarizer, err := compose.NewChain[map[string]any, *schema.Message]().
		AppendChatTemplate(tpl).
		AppendChatModel(cfg.Model).
		Compile(ctx, compose.WithGraphName("Summarizer"))
	if err != nil {
		return nil, fmt.Errorf("compile summarizer failed: %w", err)
	}

	m := &Middleware{
		counter:    defaultCounterToken,
		maxBefore:  maxBefore,
		maxRecent:  maxRecent,
		summarizer: summarizer,
	}
	if cfg.Counter != nil {
		m.counter = cfg.Counter
	}

	return m, nil
}

// ProcessMessages processes messages and returns potentially summarized messages
func (m *Middleware) ProcessMessages(ctx context.Context, messages []*schema.Message) ([]*schema.Message, error) {
	if len(messages) == 0 {
		return messages, nil
	}

	msgsToken, err := m.counter(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("count token failed: %w", err)
	}
	if len(messages) != len(msgsToken) {
		return nil, fmt.Errorf("token count mismatch: msgNum=%d, tokenCountNum=%d", len(messages), len(msgsToken))
	}

	var total int64
	for _, t := range msgsToken {
		total += t
	}

	// Trigger summarization only when exceeding threshold
	if total <= int64(m.maxBefore) {
		return messages, nil
	}

	// Split messages into blocks
	_, _, _, toolBlocks := m.splitMessages(messages, msgsToken)

	// Split into recent and older within token budget, from newest to oldest
	var recentBlocks []messageBlock
	var olderBlocks []messageBlock
	var recentTokens int64

	for i := len(toolBlocks) - 1; i >= 0; i-- {
		b := toolBlocks[i]
		if recentTokens+b.tokens > int64(m.maxRecent) {
			olderBlocks = append([]messageBlock{b}, olderBlocks...)
			continue
		}
		recentBlocks = append([]messageBlock{b}, recentBlocks...)
		recentTokens += b.tokens
	}

	// Generate summary
	olderText := joinBlocks(olderBlocks)
	_, err = m.summarizer.Invoke(ctx, map[string]any{
		"older_messages": olderText,
	})
	if err != nil {
		return nil, fmt.Errorf("summarize failed: %w", err)
	}

	// For now, just return original messages (summary logic can be enhanced)
	// TODO: Implement full summarization with summary message replacement
	return messages, nil
}

type messageBlock struct {
	msgs   []*schema.Message
	tokens int64
}

func (m *Middleware) splitMessages(messages []*schema.Message, msgsToken []int64) (systemBlock, userBlock, summaryBlock messageBlock, toolBlocks []messageBlock) {
	idx := 0

	// System messages
	if idx < len(messages) {
		msg := messages[idx]
		if msg != nil && msg.Role == schema.System {
			systemBlock.msgs = append(systemBlock.msgs, msg)
			systemBlock.tokens += msgsToken[idx]
			idx++
		}
	}

	// User messages (initial)
	for idx < len(messages) {
		msg := messages[idx]
		if msg == nil {
			idx++
			continue
		}
		if msg.Role != schema.User {
			break
		}
		userBlock.msgs = append(userBlock.msgs, msg)
		userBlock.tokens += msgsToken[idx]
		idx++
	}

	// Previous summary message
	if idx < len(messages) {
		msg := messages[idx]
		if msg != nil && msg.Role == schema.Assistant {
			if _, ok := msg.Extra[summaryMessageFlag]; ok {
				summaryBlock.msgs = append(summaryBlock.msgs, msg)
				summaryBlock.tokens += msgsToken[idx]
				idx++
			}
		}
	}

	// Tool blocks (assistant + tool messages)
	for i := idx; i < len(messages); i++ {
		msg := messages[i]
		if msg == nil {
			continue
		}

		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			b := messageBlock{msgs: []*schema.Message{msg}, tokens: msgsToken[i]}
			// Collect subsequent tool messages
			callIDs := make(map[string]struct{}, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				callIDs[tc.ID] = struct{}{}
			}
			j := i + 1
			for j < len(messages) {
				nm := messages[j]
				if nm == nil || nm.Role != schema.Tool {
					break
				}
				if nm.ToolCallID == "" {
					b.msgs = append(b.msgs, nm)
					b.tokens += msgsToken[j]
				} else {
					if _, ok := callIDs[nm.ToolCallID]; !ok {
						break
					}
					b.msgs = append(b.msgs, nm)
					b.tokens += msgsToken[j]
				}
				j++
			}
			toolBlocks = append(toolBlocks, b)
			i = j - 1
			continue
		}
		toolBlocks = append(toolBlocks, messageBlock{msgs: []*schema.Message{msg}, tokens: msgsToken[i]})
	}

	return
}

func joinBlocks(blocks []messageBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		for _, m := range b.msgs {
			sb.WriteString(renderMsg(m))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func renderMsg(m *schema.Message) string {
	if m == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("[")
	sb.WriteString(string(m.Role))
	sb.WriteString("]\n")
	if m.Content != "" {
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	if m.Role == schema.Assistant && len(m.ToolCalls) > 0 {
		for _, tc := range m.ToolCalls {
			if tc.Function.Name != "" {
				sb.WriteString("tool_call: ")
				sb.WriteString(tc.Function.Name)
				sb.WriteString("\n")
			}
			if tc.Function.Arguments != "" {
				sb.WriteString("args: ")
				sb.WriteString(tc.Function.Arguments)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

func defaultCounterToken(ctx context.Context, msgs []*schema.Message) ([]int64, error) {
	tokenNum := make([]int64, len(msgs))
	for i, m := range msgs {
		if m == nil {
			tokenNum[i] = 0
			continue
		}
		// Simple estimation: ~4 characters per token
		var length int
		length += len(string(m.Role))
		length += len(m.Content)
		if m.Role == schema.Assistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				length += len(tc.Function.Name)
				length += len(tc.Function.Arguments)
			}
		}
		tokenNum[i] = int64(length / 4)
		if tokenNum[i] == 0 && length > 0 {
			tokenNum[i] = 1
		}
	}
	return tokenNum, nil
}

const defaultSummaryPrompt = `You are a conversation summarizer. Your task is to create a concise summary of the conversation history.

Guidelines:
1. Preserve key information, decisions, and context
2. Maintain the chronological flow of the conversation
3. Include important facts, user preferences, and action items
4. Keep the summary concise but informative
5. Use clear, structured format

Output format:
- Start with "Summary of previous conversation:"
- Use bullet points for key points
- Include any pending questions or action items`
