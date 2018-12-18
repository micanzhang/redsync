package redsync

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"github.com/opencensus-integrations/redigo/redis"
)

// A DelayFunc is used to decide the amount of time to wait between retries.
type DelayFunc func(tries int) time.Duration

// A Mutex is a distributed mutual exclusion lock.
type Mutex struct {
	name   string
	expiry time.Duration

	tries     int
	delayFunc DelayFunc

	factor float64

	quorum int

	value string
	until time.Time

	nodem sync.Mutex

	pools []Pool
}

// Lock locks m. In case it returns an error on failure, you may retry to acquire the lock by calling this method again.
func (m *Mutex) Lock(ctx context.Context) error {
	m.nodem.Lock()
	defer m.nodem.Unlock()

	value, err := m.genValue()
	if err != nil {
		return err
	}

	for i := 0; i < m.tries; i++ {
		if i != 0 {
			time.Sleep(m.delayFunc(i))
		}

		start := time.Now()

		n := 0
		for _, pool := range m.pools {
			ok := m.acquire(ctx, pool, value)
			if ok {
				n++
			}
		}

		until := time.Now().Add(m.expiry - time.Now().Sub(start) - time.Duration(int64(float64(m.expiry)*m.factor)) + 2*time.Millisecond)
		if n >= m.quorum && time.Now().Before(until) {
			m.value = value
			m.until = until
			return nil
		}
		for _, pool := range m.pools {
			m.release(ctx, pool, value)
		}
	}

	return ErrFailed
}

// Unlock unlocks m and returns the status of unlock.
func (m *Mutex) Unlock(ctx context.Context) bool {
	m.nodem.Lock()
	defer m.nodem.Unlock()

	n := 0
	for _, pool := range m.pools {
		ok := m.release(ctx, pool, m.value)
		if ok {
			n++
		}
	}
	return n >= m.quorum
}

// Extend resets the mutex's expiry and returns the status of expiry extension.
func (m *Mutex) Extend(ctx context.Context) bool {
	m.nodem.Lock()
	defer m.nodem.Unlock()

	n := 0
	for _, pool := range m.pools {
		ok := m.touch(ctx, pool, m.value, int(m.expiry/time.Millisecond))
		if ok {
			n++
		}
	}
	return n >= m.quorum
}

func (m *Mutex) genValue() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func (m *Mutex) acquire(ctx context.Context, pool Pool, value string) bool {
	conn := pool.GetWithContext(ctx)
	defer conn.CloseContext(ctx)
	reply, err := redis.String(conn.Do("SET", m.name, value, "NX", "PX", int(m.expiry/time.Millisecond)))
	return err == nil && reply == "OK"
}

var deleteScript = redis.NewScript(1, `
	if redis.call("GET", KEYS[1]) == ARGV[1] then
		return redis.call("DEL", KEYS[1])
	else
		return 0
	end
`)

func (m *Mutex) release(ctx context.Context, pool Pool, value string) bool {
	conn := pool.GetWithContext(ctx)
	defer conn.CloseContext(ctx)
	status, err := deleteScript.DoContext(ctx, conn, m.name, value)
	return err == nil && status != 0
}

var touchScript = redis.NewScript(1, `
	if redis.call("GET", KEYS[1]) == ARGV[1] then
		return redis.call("SET", KEYS[1], ARGV[1], "XX", "PX", ARGV[2])
	else
		return "ERR"
	end
`)

func (m *Mutex) touch(ctx context.Context, pool Pool, value string, expiry int) bool {
	conn := pool.GetWithContext(ctx)
	defer conn.CloseContext(ctx)
	status, err := redis.String(touchScript.DoContext(ctx, conn, m.name, value, expiry))
	return err == nil && status != "ERR"
}
