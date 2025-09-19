package wadjit

import (
	"context"
	"net/netip"
	"sync"
	"time"
)

// testResolver implements TTLResolver for testing purposes.
type testResolver struct {
	mu    sync.Mutex
	addrs []netip.Addr
	ttl   time.Duration
	err   error
	calls int
}

func newTestResolver(addrs []netip.Addr, ttl time.Duration) *testResolver {
	return &testResolver{addrs: append([]netip.Addr(nil), addrs...), ttl: ttl}
}

func (r *testResolver) Lookup(
	ctx context.Context,
	host string,
) ([]netip.Addr, time.Duration, error) {
	_ = ctx
	_ = host
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	addrsCopy := append([]netip.Addr(nil), r.addrs...)
	return addrsCopy, r.ttl, r.err
}

func (r *testResolver) SetResult(addrs []netip.Addr, ttl time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addrs = append([]netip.Addr(nil), addrs...)
	r.ttl = ttl
	r.err = nil
}

func (r *testResolver) SetError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func (r *testResolver) Set(addrs []netip.Addr, ttl time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addrs = append([]netip.Addr(nil), addrs...)
	r.ttl = ttl
	r.err = err
}

func (r *testResolver) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}
