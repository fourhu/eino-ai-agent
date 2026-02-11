// Package memory provides conversation history storage implementations.
package memory

import (
	"bytes"
	"context"
	"encoding/gob"

	"github.com/cloudwego/eino/schema"
)

// Store defines the interface for conversation history storage
type Store interface {
	// Write stores messages for a session
	Write(ctx context.Context, sessionID string, msgs []*schema.Message) error
	// Read retrieves messages for a session
	Read(ctx context.Context, sessionID string) ([]*schema.Message, error)
}

// EncodeMessages serializes messages using gob
func EncodeMessages(msgs []*schema.Message) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(msgs); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecodeMessages deserializes messages using gob
func DecodeMessages(data []byte) ([]*schema.Message, error) {
	var msgs []*schema.Message
	dec := gob.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}
