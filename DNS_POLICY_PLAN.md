# DNS Resolution Policy

## Plan

### Option Set 1 – Static Resolution

- TransportControl remains available: bypass DNS entirely with a fixed netip.AddrPort.
- Add a “resolve once on initialize” mode: perform a single lookup when HTTPEndpoint.Initialize runs, cache the result, and keep the connection pool
unchanged afterwards.

### Option Set 2 – TTL-Aware Resolution (Plan A clarified)
- Build a resolver component that records each lookup’s TTL (from DNS or HAProxy metadata) and stores the expiry time.
- Before issuing a request, compare time.Now() against the cached expiry; if expired, flush idle connections (CloseIdleConnections / per-request req.Close = true) so the next dial triggers DNS + TLS.
- Log the TTL and expiry decisions for observability.

### Option Set 3 – Forced Cadence Reconnect (Plan B clarified)
- Independently of actual DNS TTLs, schedule reconnects by wall-clock cadence (e.g., every N seconds/minutes) to give operators deterministic control.
- Internally this flips the same knobs as TTL-awareness (flush/don’t reuse), but the trigger is the user-configured cadence rather than observed TTL.

Difference between A and B: A reacts to dynamic, observed TTL values; B relies on a static, user-specified interval. Both use the same mechanisms to force a fresh dial/TLS handshake, but they answer different policy needs (auto-adapt vs. operator-defined rhythm).

### Option Set 4 – Guard Rail Overrides
- Layer in an error-driven rule: on consecutive 5xx/connection failures, immediately invalidate the cache and force the next request to reconnect, regardless of TTL or cadence mode.
- Guard rails can be toggled alongside any of the above options.

### Implementation Roadmap

- Introduce a configuration struct (e.g. HTTPEndpointDNSPolicy) to capture the user’s selected mode: Static, SingleLookup, TTLRefresh, CadenceRefresh, plus guard rail settings.
- Implement a resolver cache module with locking and metrics, capable of:
    - Performing a fresh lookup and extracting TTL.
    - Serving cached records until expiry.
    - Respecting Static or SingleLookup policies.
- Extend Initialize to wire a custom RoundTripper/DialContext that consults the policy before each request: decide whether to reuse or flush connections, dial using cached IPs, or fall back to TransportControl.
- Add instrumentation to traceTimes/metadata so TTL decisions are visible to watchers.
- Provide tests for each mode (unit tests for resolver cache, integration-style tests ensuring DNSStart/TLSHandshake are triggered appropriately) and documentation updates describing how to pick a policy.

Next Steps

1. Design the policy configuration API (naming, defaults).
2. Prototype resolver cache + forced reconnect wiring.
3. Add guard-rail logic and tests.

## Policy Design

- Add type DNSRefreshMode uint8 with constants:
    - DNSRefreshDefault (current behavior: reuse transport, let Go handle DNS implicitly).
    - DNSRefreshStatic (honor TransportControl / literal address only).
    - DNSRefreshSingleLookup (resolve once during Initialize, reuse result indefinitely).
    - DNSRefreshTTL (track TTLs and force reconnect when the cached record expires).
    - DNSRefreshCadence (force reconnects every user-defined interval).
- Introduce type GuardRailPolicy struct { ConsecutiveErrorThreshold int; Window time.Duration; Action GuardRailAction } and type GuardRailAction uint8 with options such as GuardRailActionFlush (drops idle conns before next run) and GuardRailActionForceLookup (forces DNS + TLS on next attempt). Defaults: disabled (ConsecutiveErrorThreshold == 0).

### Policy Struct

- type DNSPolicy struct { Mode DNSRefreshMode; StaticAddr netip.AddrPort; Cadence time.Duration; TTLMin time.Duration; TTLMax time.Duration; AllowFallback bool; GuardRail GuardRailPolicy; }
    - StaticAddr used when Mode == DNSRefreshStatic || DNSRefreshSingleLookup (optional override).
    - Cadence applies when Mode == DNSRefreshCadence.
    - TTLMin/TTLMax impose bounds when TTL data is missing or unrealistic during DNSRefreshTTL.
    - AllowFallback controls whether to fall back to the last known address when fresh lookup fails.
- Optional hook: type DNSDecisionCallback func(ctx context.Context, decision DNSDecision) where DNSDecision includes host, mode, resolved addresses, ttl, expiresAt, guardRailTriggered bool.

### Endpoint Integration

- Extend HTTPEndpoint with dnsPolicy DNSPolicy and decisionHook DNSDecisionCallback.
- New options:
    - WithDNSPolicy(policy DNSPolicy) accepting value; validation ensures matches mode (e.g. Cadence > 0 when mode is cadence).
    - WithDNSDecisionHook(cb DNSDecisionCallback) for observability.
- Default path: if user never sets DNSPolicy, use DNSRefreshDefault.

### Resolver Interface

- Define type TTLResolver interface { Lookup(ctx context.Context, host string) (records []netip.Addr, ttl time.Duration, err error) }
    - Default implementation wraps net.Resolver and estimates TTL via time.Second * dnsMsg.Answer[0].TTL when available; exposes hooks for HAProxy TTL
    injection later.
    - DNSPolicy optionally embeds a custom resolver.

### State Tracking

- Introduce type dnsCacheEntry struct { addrs []netip.Addr; expiresAt time.Time; lastLookup time.Time }.
- Manager struct (e.g. type dnsPolicyManager struct) stored on HTTPEndpoint to coordinate when to flush the transport (CloseIdleConnections) and when to set req.Close = true.

### Guard Rail Flow

- Each httpRequest.Execute reports status to policy manager.
- When consecutive error threshold hits within window, guard rail triggers:
    - Sets a flag to force reconnect/dns lookup on next request, regardless of Mode.
    - Resets counter after successful response.

### Example Usage

endpoint := NewHTTPEndpoint(u, http.MethodGet,
    wadjit.WithDNSPolicy(wadjit.DNSPolicy{
        Mode: DNSRefreshTTL,
        TTLMin: 5 * time.Second,
        TTLMax: 2 * time.Minute,
        GuardRail: wadjit.GuardRailPolicy{
            ConsecutiveErrorThreshold: 3,
            Window: 30 * time.Second,
            Action: wadjit.GuardRailActionForceLookup,
        },
    }),
    wadjit.WithDNSDecisionHook(logDecision),
)

### Validation Rules

- StaticAddr must be non-zero for DNSRefreshStatic.
- Cadence > 0 for DNSRefreshCadence.
- TTLMin <= TTLMax, non-negative.
- Guard rail window optional; if zero, treat as unlimited horizon.

## API Notes (implemented)
- `WithDNSPolicy(policy wadjit.DNSPolicy)` attaches DNS refresh behavior to an HTTP endpoint.
- `WithDNSDecisionHook(func(ctx context.Context, decision wadjit.DNSDecision))` observes per-request decisions.
- `DNSPolicy` supports modes: Default, Static, SingleLookup, TTL, Cadence; optional guard rails react to consecutive failures.
- DNS decisions surface in `WatcherResponse.Metadata().DNS` for downstream monitoring.
