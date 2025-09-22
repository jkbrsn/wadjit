package wadjit

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDNSPolicyManagerDefaultMode(t *testing.T) {
	u := &url.URL{Scheme: "https", Host: "default.example"}

	mgr := newDNSPolicyManager(DNSPolicy{Mode: DNSRefreshDefault}, nil)
	transport := &http.Transport{}

	ctx, force, err := mgr.prepareRequest(context.Background(), u, transport)
	require.NoError(t, err)
	assert.False(t, force)
	assert.Nil(t, ctx.Value(dnsPlanKey{}))
}

func TestDNSPolicyManagerTTLRefresh(t *testing.T) {
	host := "example.com"
	u := &url.URL{Scheme: "https", Host: host}

	policy := DNSPolicy{Mode: DNSRefreshTTL, TTLMin: time.Second, TTLMax: 10 * time.Second}
	mgr := newDNSPolicyManager(policy, nil)
	mgr.resolver = newTestResolver([]netip.Addr{netip.MustParseAddr("192.0.2.10")}, 5*time.Second)

	transport := &http.Transport{}

	ctx, force, err := mgr.prepareRequest(context.Background(), u, transport)
	require.NoError(t, err)
	assert.True(t, force, "first lookup should force new connection")

	plan, ok := ctx.Value(dnsPlanKey{}).(dnsDialPlan)
	require.True(t, ok)
	assert.Equal(t, "192.0.2.10:443", plan.target)

	decision, ok := ctx.Value(dnsDecisionKey{}).(DNSDecision)
	require.True(t, ok)
	assert.Equal(t, DNSRefreshTTL, decision.Mode)
	assert.Equal(t, 5*time.Second, decision.TTL)
	assert.False(t, decision.GuardRailTriggered)

	ctx, force, err = mgr.prepareRequest(context.Background(), u, transport)
	require.NoError(t, err)
	assert.False(t, force, "within TTL should not force reconnect")
	plan, ok = ctx.Value(dnsPlanKey{}).(dnsDialPlan)
	require.True(t, ok)
	assert.Equal(t, "192.0.2.10:443", plan.target)
}

func TestDNSPolicyManagerGuardRailForcesFlush(t *testing.T) {
	host := "example.org"
	u := &url.URL{Scheme: "http", Host: host}

	policy := DNSPolicy{
		Mode:      DNSRefreshCadence,
		Cadence:   10 * time.Second,
		GuardRail: GuardRailPolicy{ConsecutiveErrorThreshold: 1, Action: GuardRailActionFlush},
	}
	mgr := newDNSPolicyManager(policy, nil)
	mgr.resolver = newTestResolver([]netip.Addr{netip.MustParseAddr("198.51.100.1")}, 0)

	transport := &http.Transport{}

	// Initial request seeds the cache
	_, _, err := mgr.prepareRequest(context.Background(), u, transport)
	require.NoError(t, err)
	mgr.observeResult(200, nil) // reset guard state

	// Trigger guard rail
	mgr.observeResult(500, nil)

	ctx, force, err := mgr.prepareRequest(context.Background(), u, transport)
	require.NoError(t, err)
	assert.True(t, force, "guard rail should force reconnect")

	decision, ok := ctx.Value(dnsDecisionKey{}).(DNSDecision)
	require.True(t, ok)
	assert.True(t, decision.GuardRailTriggered)
}

func TestDNSPolicyManagerCadenceRefresh(t *testing.T) {
	host := "cadence.example"
	u := &url.URL{Scheme: "https", Host: host}

	policy := DNSPolicy{Mode: DNSRefreshCadence, Cadence: 40 * time.Millisecond}
	mgr := newDNSPolicyManager(policy, nil)
	mgr.resolver = newTestResolver([]netip.Addr{netip.MustParseAddr("198.51.100.42")}, 0)

	transport := &http.Transport{}

	ctx, force, err := mgr.prepareRequest(context.Background(), u, transport)
	require.NoError(t, err)
	assert.True(t, force, "first cadence lookup should force new connection")

	decision, ok := ctx.Value(dnsDecisionKey{}).(DNSDecision)
	require.True(t, ok)
	firstExpiry := decision.ExpiresAt
	require.False(t, firstExpiry.IsZero())

	ctx, force, err = mgr.prepareRequest(context.Background(), u, transport)
	require.NoError(t, err)
	assert.False(t, force, "within cadence window should reuse connection")
	decision, ok = ctx.Value(dnsDecisionKey{}).(DNSDecision)
	require.True(t, ok)
	assert.Equal(t, firstExpiry, decision.ExpiresAt)

	time.Sleep(policy.Cadence + 10*time.Millisecond)

	ctx, force, err = mgr.prepareRequest(context.Background(), u, transport)
	require.NoError(t, err)
	assert.True(t, force, "after cadence expiry should force reconnect")
	decision, ok = ctx.Value(dnsDecisionKey{}).(DNSDecision)
	require.True(t, ok)
	assert.True(t, decision.ExpiresAt.After(firstExpiry))
}

func TestDNSPolicyManagerTTLFallbackGuardRail(t *testing.T) {
	host := "ttl-guard.example"
	u := &url.URL{Scheme: "https", Host: host}

	policy := DNSPolicy{
		Mode:   DNSRefreshTTL,
		TTLMin: 15 * time.Millisecond,
		TTLMax: 50 * time.Millisecond,
		GuardRail: GuardRailPolicy{
			ConsecutiveErrorThreshold: 2,
			Window:                    200 * time.Millisecond,
			Action:                    GuardRailActionForceLookup,
		},
	}
	mgr := newDNSPolicyManager(policy, nil)
	initialAddr := netip.MustParseAddr("192.0.2.80")
	resolver := newTestResolver([]netip.Addr{initialAddr}, 20*time.Millisecond)
	mgr.resolver = resolver

	transport := &http.Transport{}

	ctx, force, err := mgr.prepareRequest(context.Background(), u, transport)
	require.NoError(t, err)
	assert.True(t, force, "first TTL lookup should force new connection")
	decision, ok := ctx.Value(dnsDecisionKey{}).(DNSDecision)
	require.True(t, ok)
	firstExpiry := decision.ExpiresAt
	require.False(t, firstExpiry.IsZero())
	assert.Equal(t, initialAddr, decision.ResolvedAddrs[0])

	time.Sleep(2 * policy.TTLMin)

	resolver.SetError(errors.New("lookup failed"))

	ctx, force, err = mgr.prepareRequest(context.Background(), u, transport)
	require.NoError(t, err)
	assert.False(t, force, "fallback should reuse cached address when lookup fails")
	decision, ok = ctx.Value(dnsDecisionKey{}).(DNSDecision)
	require.True(t, ok)
	require.NotNil(t, decision.Err)
	assert.Equal(t, initialAddr, decision.ResolvedAddrs[0])
	assert.Equal(t, firstExpiry, decision.ExpiresAt)

	mgr.observeResult(503, nil)
	mgr.observeResult(500, nil)

	newAddr := netip.MustParseAddr("192.0.2.81")
	resolver.SetResult([]netip.Addr{newAddr}, 30*time.Millisecond)

	ctx, force, err = mgr.prepareRequest(context.Background(), u, transport)
	require.NoError(t, err)
	assert.True(t, force, "guard rail should force new lookup after threshold")
	decision, ok = ctx.Value(dnsDecisionKey{}).(DNSDecision)
	require.True(t, ok)
	assert.True(t, decision.GuardRailTriggered)
	assert.Equal(t, newAddr, decision.ResolvedAddrs[0])
	assert.True(t, decision.ExpiresAt.After(firstExpiry))
	assert.GreaterOrEqual(t, resolver.Calls(), 2)
}

func TestDNSPolicyManagerTTLFallbackDisabled(t *testing.T) {
	host := "ttl-disable.example"
	u := &url.URL{Scheme: "https", Host: host}

	policy := DNSPolicy{
		Mode:            DNSRefreshTTL,
		TTLMin:          10 * time.Millisecond,
		TTLMax:          30 * time.Millisecond,
		DisableFallback: true,
	}
	mgr := newDNSPolicyManager(policy, nil)
	initialAddr := netip.MustParseAddr("192.0.2.90")
	resolver := newTestResolver([]netip.Addr{initialAddr}, 15*time.Millisecond)
	mgr.resolver = resolver

	transport := &http.Transport{}

	_, force, err := mgr.prepareRequest(context.Background(), u, transport)
	require.NoError(t, err)
	assert.True(t, force, "first TTL lookup should force new connection")

	time.Sleep(2 * policy.TTLMin)
	resolver.SetError(errors.New("lookup failed"))

	_, force, err = mgr.prepareRequest(context.Background(), u, transport)
	require.Error(t, err)
	assert.ErrorContains(t, err, "lookup failed")
	assert.False(t, force, "no dial plan provided on error")
}

func TestDNSPolicyManagerTTLZeroTTLExpiresImmediately(t *testing.T) {
	host := "ttl-zero.example"
	u := &url.URL{Scheme: "https", Host: host}

	policy := DNSPolicy{Mode: DNSRefreshTTL}
	mgr := newDNSPolicyManager(policy, nil)

	addr := netip.MustParseAddr("203.0.113.55")
	resolver := newTestResolver([]netip.Addr{addr}, 0)
	mgr.resolver = resolver

	transport := &http.Transport{}

	ctx, force, err := mgr.prepareRequest(context.Background(), u, transport)
	require.NoError(t, err)
	assert.True(t, force, "first lookup should force new connection")

	decision, ok := ctx.Value(dnsDecisionKey{}).(DNSDecision)
	require.True(t, ok)
	assert.Equal(t, time.Duration(0), decision.TTL)
	require.False(t, decision.ExpiresAt.IsZero())

	firstCalls := resolver.Calls()
	assert.Equal(t, 1, firstCalls)

	ctx, force, err = mgr.prepareRequest(context.Background(), u, transport)
	require.NoError(t, err)
	assert.True(t, force, "zero TTL entries must trigger new lookup on next request")
	decision, ok = ctx.Value(dnsDecisionKey{}).(DNSDecision)
	require.True(t, ok)
	assert.Equal(t, time.Duration(0), decision.TTL)
	assert.Equal(t, 2, resolver.Calls(), "resolver should run again after zero TTL cache entry")
}

func TestHTTPTaskResponseMetadataIncludesDNS(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)

	decision := DNSDecision{
		Host:               "example.com",
		Mode:               DNSRefreshTTL,
		TTL:                3 * time.Second,
		ExpiresAt:          time.Now().Add(3 * time.Second),
		ResolvedAddrs:      []netip.Addr{netip.MustParseAddr("203.0.113.2")},
		GuardRailTriggered: true,
	}
	ctx := context.WithValue(request.Context(), dnsDecisionKey{}, decision)
	request = request.WithContext(ctx)

	resp := &http.Response{
		StatusCode:    200,
		Header:        make(http.Header),
		Request:       request,
		ContentLength: 123,
	}
	h := NewHTTPTaskResponse(&net.TCPAddr{}, resp)

	md := h.Metadata()
	require.NotNil(t, md.DNS)
	assert.Equal(t, DNSRefreshTTL, md.DNS.Mode)
	assert.Equal(t, decision.TTL, md.DNS.TTL)
	assert.Equal(t, decision.ExpiresAt, md.DNS.ExpiresAt)
	assert.Equal(t, decision.GuardRailTriggered, md.DNS.GuardRailTriggered)
	require.Len(t, md.DNS.ResolvedAddrs, 1)
	assert.Equal(t, decision.ResolvedAddrs[0], md.DNS.ResolvedAddrs[0])
}

func TestDNSPolicyValidateErrors(t *testing.T) {
	err := (DNSPolicy{Mode: DNSRefreshStatic}).Validate()
	assert.Error(t, err)

	err = (DNSPolicy{Mode: DNSRefreshCadence}).Validate()
	assert.Error(t, err)

	err = (DNSPolicy{Mode: DNSRefreshCadence, Cadence: -time.Second}).Validate()
	assert.Error(t, err)

	err = (DNSPolicy{Mode: DNSRefreshTTL, TTLMin: 2 * time.Second, TTLMax: time.Second}).Validate()
	assert.Error(t, err)
}
