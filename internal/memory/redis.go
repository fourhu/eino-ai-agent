// Package memory provides conversation history storage implementations.
package memory

import (
	"context"
	"fmt"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"

	"github.com/fourhu/eino-ai-agent/internal/logger"
)

// RedisStore persists conversation history in Redis
type RedisStore struct {
	cli    *redis.Client
	prefix string
}

// NewRedisStore creates a new Redis-backed store with an existing client
func NewRedisStore(cli *redis.Client, prefix string) *RedisStore {
	if prefix == "" {
		prefix = "eino:session:"
	}
	return &RedisStore{
		cli:    cli,
		prefix: prefix,
	}
}

// NewRedisStoreFromAddress creates a new Redis-backed store from address
// This function tests the connection before returning
func NewRedisStoreFromAddress(ctx context.Context, address, prefix string) (*RedisStore, error) {
	if prefix == "" {
		prefix = "eino:session:"
	}

	logger.Debugf("[Memory:Redis] Connecting to Redis at %s", address)

	cli := redis.NewClient(&redis.Options{
		Addr:     address,
		Protocol: 2,
	})

	// Test connection
	if err := cli.Ping(ctx).Err(); err != nil {
		cli.Close()
		return nil, fmt.Errorf("failed to connect to Redis at %s: %w", address, err)
	}

	logger.Debugf("[Memory:Redis] Successfully connected to Redis at %s", address)

	return &RedisStore{
		cli:    cli,
		prefix: prefix,
	}, nil
}

// Close closes the Redis client connection
func (s *RedisStore) Close() error {
	if s.cli != nil {
		return s.cli.Close()
	}
	return nil
}

// Write encodes and stores messages using Redis SET
func (s *RedisStore) Write(ctx context.Context, sessionID string, msgs []*schema.Message) error {
	key := s.prefix + sessionID
	logger.Debugf("[Memory:Redis] Writing session %s (%d messages)", sessionID, len(msgs))

	b, err := EncodeMessages(msgs)
	if err != nil {
		logger.Errorf("[Memory:Redis] Failed to encode messages for session %s: %v", sessionID, err)
		return err
	}

	if err := s.cli.Set(ctx, key, b, 0).Err(); err != nil {
		logger.Errorf("[Memory:Redis] Failed to write session %s: %v", sessionID, err)
		return err
	}

	logger.Debugf("[Memory:Redis] Successfully wrote session %s (%d bytes)", sessionID, len(b))
	return nil
}

// Read returns decoded messages from Redis GET; returns nil if not found
func (s *RedisStore) Read(ctx context.Context, sessionID string) ([]*schema.Message, error) {
	key := s.prefix + sessionID
	logger.Debugf("[Memory:Redis] Reading session %s", sessionID)

	res, err := s.cli.Get(ctx, key).Bytes()
	if err == redis.Nil {
		logger.Debugf("[Memory:Redis] Session %s not found", sessionID)
		return nil, nil
	}
	if err != nil {
		logger.Errorf("[Memory:Redis] Failed to read session %s: %v", sessionID, err)
		return nil, err
	}

	msgs, err := DecodeMessages(res)
	if err != nil {
		logger.Errorf("[Memory:Redis] Failed to decode messages for session %s: %v", sessionID, err)
		return nil, err
	}

	logger.Debugf("[Memory:Redis] Successfully read session %s (%d messages)", sessionID, len(msgs))
	return msgs, nil
}

// NewMiniRedisClient starts an embedded Redis server for local demos/tests
func NewMiniRedisClient() (*redis.Client, func(), error) {
	logger.Debug("[Memory:Redis] Starting embedded miniredis server")

	srv, err := miniredis.Run()
	if err != nil {
		logger.Errorf("[Memory:Redis] Failed to start miniredis: %v", err)
		return nil, nil, err
	}

	cli := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	closer := func() {
		logger.Debug("[Memory:Redis] Stopping embedded miniredis server")
		srv.Close()
	}

	logger.Debugf("[Memory:Redis] Embedded miniredis started at %s", srv.Addr())
	return cli, closer, nil
}
