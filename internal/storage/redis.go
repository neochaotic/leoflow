package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/neochaotic/leoflow/internal/config"
)

// Redis wraps a go-redis client used for XCom and locks.
type Redis struct {
	Client *redis.Client
}

// redisOptions parses a redis URL into client options.
func redisOptions(url string) (*redis.Options, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parsing redis url: %w", err)
	}
	return opts, nil
}

// NewRedis connects to Redis and verifies connectivity.
func NewRedis(ctx context.Context, cfg config.RedisSection) (*Redis, error) {
	opts, err := redisOptions(cfg.URL)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)
	if perr := client.Ping(ctx).Err(); perr != nil {
		return nil, errors.Join(fmt.Errorf("pinging redis: %w", perr), client.Close())
	}
	return &Redis{Client: client}, nil
}

// Ping checks Redis connectivity (used by /readyz).
func (r *Redis) Ping(ctx context.Context) error {
	return r.Client.Ping(ctx).Err()
}

// Close releases the Redis client.
func (r *Redis) Close() error {
	return r.Client.Close()
}
