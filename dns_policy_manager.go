package wadjit

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"time"
)

type dnsCacheEntry struct {
	addrs      []netip.Addr
	expiresAt  time.Time
	lastLookup time.Time
}

type guardRailState struct {
	policy       DNSGuardRailPolicy
	count        int
	firstFailure time.Time
	lastFailure  time.Time
	triggered    bool
}

type dnsPolicyManager struct {
	policy   DNSPolicy
	resolver TTLResolver
	hook     DNSDecisionCallback

	mu          sync.Mutex
	cache       dnsCacheEntry
	cadenceNext time.Time
	forceLookup bool
	guard       guardRailState
}

// newDNSPolicyManager wires the supplied policy and optional decision hook into a manager ready to
// drive dial decisions.
func newDNSPolicyManager(policy DNSPolicy, hook DNSDecisionCallback) *dnsPolicyManager {
	mgr := &dnsPolicyManager{policy: policy, hook: hook}
	if mgr.policy.Resolver != nil {
		mgr.resolver = mgr.policy.Resolver
	} else {
		mgr.resolver = newDefaultTTLResolver()
	}
	mgr.guard.policy = policy.GuardRail
	return mgr
}

type dnsPlanKey struct{}

type dnsDialPlan struct {
	target string
}

type dnsDecisionKey struct{}

type dnsResolveOutcome struct {
	address      string
	forceNewConn bool
	decision     *DNSDecision
}

const serverErrorThreshold = 500

func nonNegativeDuration(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

// prepareRequest determines the dial target for the upcoming request, applies guard-rail actions,
// and enriches the context with DNS decision data.
func (m *dnsPolicyManager) prepareRequest(
	ctx context.Context,
	u *url.URL,
	transport *http.Transport,
) (context.Context, bool, error) {
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = defaultPort(u.Scheme)
	}
	if port == "" {
		return ctx, false, errors.New("cannot determine port for URL")
	}

	outcome, err := m.resolve(ctx, host, port)
	if err != nil {
		if outcome.decision != nil && m.hook != nil {
			outcome.decision.Err = err
			m.hook(ctx, *outcome.decision)
		}
		return ctx, false, err
	}
	guardTriggered := m.consumeGuardTrigger()
	decision := outcome.decision
	if decision == nil && (m.hook != nil || guardTriggered) {
		decision = &DNSDecision{Host: host, Mode: m.policy.Mode}
	}

	if guardTriggered && decision != nil {
		outcome.forceNewConn = true
		decision.GuardRailTriggered = true
	}

	resultCtx := ctx
	if outcome.address != "" {
		plan := dnsDialPlan{target: outcome.address}
		resultCtx = context.WithValue(resultCtx, dnsPlanKey{}, plan)
	}

	if decision != nil {
		resultCtx = context.WithValue(resultCtx, dnsDecisionKey{}, *decision)
		if m.hook != nil {
			m.hook(resultCtx, *decision)
		}
	}

	if outcome.forceNewConn {
		transport.CloseIdleConnections()
	}

	return resultCtx, outcome.forceNewConn, nil
}

// resolve selects the connection strategy for the given host/port based on the configured refresh
// mode.
func (m *dnsPolicyManager) resolve(
	ctx context.Context,
	host,
	port string,
) (dnsResolveOutcome, error) {
	switch m.policy.Mode {
	case DNSRefreshDefault:
		return dnsResolveOutcome{}, nil
	case DNSRefreshStatic:
		addr := m.policy.StaticAddr
		if (addr == netip.AddrPort{}) {
			return dnsResolveOutcome{}, errors.Join(ErrInvalidDNSPolicy,
				errors.New("static address missing"))
		}
		decision := &DNSDecision{
			Host:          host,
			Mode:          m.policy.Mode,
			ResolvedAddrs: []netip.Addr{addr.Addr()},
		}
		return dnsResolveOutcome{address: addr.String(), decision: decision}, nil
	case DNSRefreshSingleLookup:
		return m.resolveSingle(ctx, host, port)
	case DNSRefreshTTL:
		return m.resolveTTL(ctx, host, port)
	case DNSRefreshCadence:
		return m.resolveCadence(ctx, host, port)
	default:
		return dnsResolveOutcome{},
			errors.Join(ErrInvalidDNSPolicy, errors.New("unsupported DNS mode"))
	}
}

// resolveSingle caches the first lookup result and reuses it for subsequent requests.
func (m *dnsPolicyManager) resolveSingle(
	ctx context.Context,
	host,
	port string,
) (dnsResolveOutcome, error) {
	m.mu.Lock()
	cached := m.cache
	m.mu.Unlock()

	if len(cached.addrs) > 0 {
		addr := net.JoinHostPort(cached.addrs[0].String(), port)
		decision := DNSDecision{Host: host, Mode: m.policy.Mode, ResolvedAddrs: cached.addrs}
		return dnsResolveOutcome{address: addr, decision: &decision}, nil
	}

	addrs, ttl, err := m.lookup(ctx, host)
	if err != nil {
		return dnsResolveOutcome{}, err
	}
	entry := dnsCacheEntry{addrs: addrs, lastLookup: time.Now()}

	m.mu.Lock()
	m.cache = entry
	m.mu.Unlock()

	addr := net.JoinHostPort(addrs[0].String(), port)
	decision := DNSDecision{Host: host, Mode: m.policy.Mode, ResolvedAddrs: addrs, TTL: ttl}
	return dnsResolveOutcome{address: addr, forceNewConn: true, decision: &decision}, nil
}

// resolveTTL refreshes addresses when the stored TTL expires while optionally preserving the last
// good answer.
func (m *dnsPolicyManager) resolveTTL(
	ctx context.Context,
	host,
	port string,
) (dnsResolveOutcome, error) {
	now := time.Now()
	m.mu.Lock()
	cached := m.cache
	forceLookup := m.forceLookup || len(cached.addrs) == 0 ||
		(!cached.expiresAt.IsZero() && now.After(cached.expiresAt))
	m.forceLookup = false
	m.mu.Unlock()

	if !forceLookup {
		addr := net.JoinHostPort(cached.addrs[0].String(), port)
		ttl := nonNegativeDuration(cached.expiresAt.Sub(cached.lastLookup))
		decision := DNSDecision{
			Host:          host,
			Mode:          m.policy.Mode,
			ResolvedAddrs: cached.addrs,
			TTL:           ttl,
			ExpiresAt:     cached.expiresAt,
		}
		return dnsResolveOutcome{address: addr, decision: &decision}, nil
	}

	addrs, ttl, err := m.lookup(ctx, host)
	if err != nil {
		if len(cached.addrs) > 0 && m.policy.fallbackEnabled() {
			addr := net.JoinHostPort(cached.addrs[0].String(), port)
			ttl := nonNegativeDuration(cached.expiresAt.Sub(cached.lastLookup))
			decision := DNSDecision{
				Host:          host,
				Mode:          m.policy.Mode,
				ResolvedAddrs: cached.addrs,
				TTL:           ttl,
				ExpiresAt:     cached.expiresAt,
				Err:           err,
			}
			return dnsResolveOutcome{address: addr, decision: &decision}, nil
		}
		return dnsResolveOutcome{}, err
	}

	ttl = m.policy.normalizeTTL(ttl)
	entry := dnsCacheEntry{addrs: addrs, lastLookup: now}
	if ttl <= 0 {
		entry.expiresAt = now
	} else {
		entry.expiresAt = now.Add(ttl)
	}

	m.mu.Lock()
	m.cache = entry
	m.mu.Unlock()

	decision := DNSDecision{
		Host:          host,
		Mode:          m.policy.Mode,
		ResolvedAddrs: addrs,
		TTL:           ttl,
		ExpiresAt:     entry.expiresAt,
	}
	addr := net.JoinHostPort(addrs[0].String(), port)
	return dnsResolveOutcome{address: addr, forceNewConn: true, decision: &decision}, nil
}

// resolveCadence forces DNS refreshes on a fixed schedule independent of observed TTLs.
func (m *dnsPolicyManager) resolveCadence(
	ctx context.Context,
	host,
	port string,
) (dnsResolveOutcome, error) {
	now := time.Now()

	m.mu.Lock()
	cached := m.cache
	cadenceNext := m.cadenceNext
	forceLookup := m.forceLookup || len(cached.addrs) == 0 ||
		(!cadenceNext.IsZero() && now.After(cadenceNext))
	m.forceLookup = false
	m.mu.Unlock()

	if !forceLookup && len(cached.addrs) > 0 {
		addr := net.JoinHostPort(cached.addrs[0].String(), port)
		decision := DNSDecision{
			Host:          host,
			Mode:          m.policy.Mode,
			ResolvedAddrs: cached.addrs,
			ExpiresAt:     cadenceNext,
		}
		return dnsResolveOutcome{address: addr, decision: &decision}, nil
	}

	addrs, ttl, err := m.lookup(ctx, host)
	if err != nil {
		if len(cached.addrs) > 0 && m.policy.fallbackEnabled() {
			addr := net.JoinHostPort(cached.addrs[0].String(), port)
			decision := DNSDecision{
				Host:          host,
				Mode:          m.policy.Mode,
				ResolvedAddrs: cached.addrs,
				ExpiresAt:     cadenceNext,
				Err:           err,
			}
			return dnsResolveOutcome{address: addr, decision: &decision}, nil
		}
		return dnsResolveOutcome{}, err
	}

	entry := dnsCacheEntry{addrs: addrs, lastLookup: now}
	next := now.Add(m.policy.Cadence)

	m.mu.Lock()
	m.cache = entry
	m.cadenceNext = next
	m.mu.Unlock()

	decision := DNSDecision{
		Host:          host,
		Mode:          m.policy.Mode,
		ResolvedAddrs: addrs,
		TTL:           ttl,
		ExpiresAt:     next,
	}
	addr := net.JoinHostPort(addrs[0].String(), port)
	return dnsResolveOutcome{address: addr, forceNewConn: true, decision: &decision}, nil
}

// lookup delegates to the configured resolver and ensures a non-empty address list is returned.
func (m *dnsPolicyManager) lookup(
	ctx context.Context,
	host string,
) ([]netip.Addr, time.Duration, error) {
	if m.resolver == nil {
		return nil, 0, errors.New("resolver not configured")
	}
	addrs, ttl, err := m.resolver.Lookup(ctx, host)
	if err != nil {
		return nil, 0, err
	}
	if len(addrs) == 0 {
		return nil, 0, errors.New("resolver returned no addresses")
	}
	return addrs, ttl, nil
}

// dialContext injects the manager's dial plan into the provided net.Dialer.
func (*dnsPolicyManager) dialContext(
	dialer *net.Dialer,
) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if plan, ok := ctx.Value(dnsPlanKey{}).(dnsDialPlan); ok {
			if plan.target != "" {
				address = plan.target
			}
		}
		return dialer.DialContext(ctx, network, address)
	}
}

// observeResult records request outcomes to drive guard-rail thresholds.
func (m *dnsPolicyManager) observeResult(statusCode int, resultErr error) {
	if !m.guard.policy.Enabled() {
		return
	}
	now := time.Now()
	if resultErr != nil || statusCode >= serverErrorThreshold {
		m.registerGuardFailure(now)
		return
	}
	m.resetGuard()
}

// registerGuardFailure increments guard counters and triggers configured actions once thresholds
// are met.
func (m *dnsPolicyManager) registerGuardFailure(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	policy := m.guard.policy
	if !policy.Enabled() {
		return
	}

	if policy.Window > 0 {
		if m.guard.firstFailure.IsZero() || now.Sub(m.guard.firstFailure) > policy.Window {
			m.guard.count = 0
			m.guard.firstFailure = now
		}
	} else if m.guard.firstFailure.IsZero() {
		m.guard.firstFailure = now
	}

	m.guard.count++
	m.guard.lastFailure = now

	if m.guard.count >= policy.ConsecutiveErrorThreshold {
		m.guard.triggered = true
		m.guard.count = 0
		m.guard.firstFailure = time.Time{}
		if policy.Action == DNSGuardRailActionForceLookup {
			m.forceLookup = true
		}
	}
}

// resetGuard clears the failure counters after a successful request.
func (m *dnsPolicyManager) resetGuard() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.guard.count = 0
	m.guard.firstFailure = time.Time{}
	m.guard.lastFailure = time.Time{}
}

// consumeGuardTrigger reports whether a guard action should influence the current request and
// resets the flag.
func (m *dnsPolicyManager) consumeGuardTrigger() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.guard.triggered {
		return false
	}
	m.guard.triggered = false
	return m.guard.policy.Action != DNSGuardRailActionNone
}

type policyTransport struct {
	base *http.Transport
	mgr  *dnsPolicyManager
}

// RoundTrip injects DNS policy context into the request before delegating to the underlying
// transport.
func (p *policyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, forceClose, err := p.mgr.prepareRequest(req.Context(), req.URL, p.base)
	if err != nil {
		return nil, err
	}

	newReq := req.Clone(ctx)
	if forceClose {
		newReq.Close = true
	}

	return p.base.RoundTrip(newReq)
}

// defaultPort maps common URL schemes to their implicit TCP port.
func defaultPort(scheme string) string {
	switch scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}
