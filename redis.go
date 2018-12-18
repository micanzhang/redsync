package redsync

import (
	"context"

	"github.com/opencensus-integrations/redigo/redis"
)

// A Pool maintains a pool of Redis connections.
type Pool interface {
	GetWithContext(context.Context) redis.ConnWithContext
}
