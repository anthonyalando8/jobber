package queue

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// NewRedisPool creates a Redis client pool from a URL
func NewRedisPool(redisURL string) (*redis.Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis URL: %w", err)
	}

	client := redis.NewClient(opts)
	return client, nil
}

// Job represents a queued job
type Job struct {
	Type      string // enrich|draft|send
	CompanyID int64
	Payload   string
}

// Enqueue adds a job to a queue
func Enqueue(client *redis.Client, ctx context.Context, queueName string, jobID string) error {
	return client.LPush(ctx, queueName, jobID).Err()
}

// Dequeue pops a job from the queue safely
func Dequeue(client *redis.Client, ctx context.Context, queueName string) (string, error) {
	// BRPOPLPUSH is used for safety: moves job from queue to processing list
	result, err := client.BRPopLPush(ctx, queueName, queueName+":processing", 0).Result()
	if err != nil {
		return "", err
	}
	return result, nil
}
