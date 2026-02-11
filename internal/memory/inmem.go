// Package memory provides conversation history storage implementations.
package memory

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/schema"
)

// InMemoryStore stores conversation history in memory
type InMemoryStore struct {
	data map[string][]*schema.Message
	mu   sync.RWMutex
}

// NewInMemoryStore creates a new in-memory store
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		data: make(map[string][]*schema.Message),
	}
}

// Write stores messages for a session
func (s *InMemoryStore) Write(ctx context.Context, sessionID string, msgs []*schema.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Make a copy to avoid external modifications
	msgsCopy := make([]*schema.Message, len(msgs))
	copy(msgsCopy, msgs)
	s.data[sessionID] = msgsCopy
	return nil
}

// Read retrieves messages for a session
func (s *InMemoryStore) Read(ctx context.Context, sessionID string) ([]*schema.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	msgs, exists := s.data[sessionID]
	if !exists {
		return nil, nil
	}

	// Return a copy to avoid external modifications
	msgsCopy := make([]*schema.Message, len(msgs))
	copy(msgsCopy, msgs)
	return msgsCopy, nil
}
