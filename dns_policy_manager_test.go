package wadjit

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubTTLResolver struct {
	addrs []netip.Addr
	ttl   time.Duration
	err   error
}

func (s stubTTLResolver) Lookup(ctx context.Context, host string) ([]netip.Addr, time.Duration, error) {
	return s.addrs, s.ttl, s.err
}

func TestDNSPolicyManagerTTLRefresh(t *testing.T) {
	host := "example.com"
	u := &url.URL{Scheme: "https", Host: host}

	policy := DNSPolicy{Mode: DNSRefreshTTL, TTLMin: time.Second, TTLMax: 10 * time.Second}
	mgr := newDNSPolicyManager(policy, nil)
	mgr.resolver = stubTTLResolver{
		addrs: []netip.Addr{netip.MustParseAddr("192.0.2.10")},
		ttl:   5 * time.Second,
	}

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
	mgr.resolver = stubTTLResolver{addrs: []netip.Addr{netip.MustParseAddr("198.51.100.1")}}

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

	resp := &http.Response{StatusCode: 200, Header: make(http.Header), Request: request, ContentLength: 123}
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
