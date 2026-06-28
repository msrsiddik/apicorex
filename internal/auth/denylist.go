package auth

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// Denylist tracks revoked access-token JTIs in Redis (logout). Each entry
// self-expires when the token would have expired anyway.
type Denylist struct {
	rdb *redis.Client
}

func NewDenylist(redisURL string) (*Denylist, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	return &Denylist{rdb: redis.NewClient(opt)}, nil
}

// IsRevoked reports whether the given JTI has been denied (logged out).
func (d *Denylist) IsRevoked(ctx context.Context, jti string) bool {
	if d == nil || jti == "" {
		return false
	}
	n, err := d.rdb.Exists(ctx, key(jti)).Result()
	return err == nil && n > 0
}

// Revoke adds a JTI to the denylist with a TTL equal to its remaining lifetime.
func (d *Denylist) Revoke(ctx context.Context, jti string, ttl time.Duration) error {
	if d == nil || jti == "" || ttl <= 0 {
		return nil
	}
	return d.rdb.Set(ctx, key(jti), "1", ttl).Err()
}

func key(jti string) string { return "denylist:jti:" + jti }
