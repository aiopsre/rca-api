package redisx

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultConnectTimeout = 2 * time.Second

var errNilContext = errors.New("nil context")

// NewClient initializes and health-checks a redis client when enabled.
func NewClient(ctx context.Context, opts RedisOptions) (*redis.Client, error) {
	opts.ApplyDefaults()
	if ctx == nil {
		return nil, errNilContext
	}

	client := redis.NewClient(&redis.Options{
		Addr:     opts.Addr,
		Password: opts.Password,
		DB:       opts.DB,
	})

	pingCtx, cancel := context.WithTimeout(ctx, defaultConnectTimeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}

	return client, nil
}
