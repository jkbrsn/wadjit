# DNS Error Handling Implementation Plan

## Problem Statement

DNS resolution errors are not consistently propagated to the response channel when HTTP tasks execute. This results in missing error responses for certain DNS policy configurations, making it difficult for clients to detect and respond to DNS failures.

## Observed Behavior

### Working Cases
- **DNSRefreshStatic**: Errors are properly reported when targeting non-existing resources
  - Static mode bypasses DNS entirely
  - Connection failures at TCP level bubble up through `client.Do()`
  - Error responses are sent to response channel via `task_http.go:197`

### Failing Cases
- **DNSRefreshTTL**: DNS lookup failures may not generate error responses
- **DNSRefreshCadence**: DNS lookup failures may not generate error responses
- **DNSRefreshDefault** (suspected): May inherit problematic behavior in certain configurations

## Root Cause Analysis

### DNS Fallback Logic Swallows Errors

When DNS lookups fail in TTL or Cadence modes and fallback is enabled, the error is recorded in the `DNSDecision` struct but the request continues with a cached address. This prevents error propagation to the response channel.

#### Code Location: dns_policy_manager.go:234-249 (resolveTTL)

```go
addrs, ttl, lookupDur, err := m.lookup(ctx, host)
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
            Err:           err,  // ← Error recorded here
        }
        return dnsResolveOutcome{address: addr, decision: &decision}, nil  // ← Returns SUCCESS
    }
    return dnsResolveOutcome{}, err
}
```

**What happens:**
1. DNS lookup fails (`err != nil`)
2. Code checks if cached address exists and fallback is enabled
3. Returns cached address with `nil` error
4. DNS error only stored in `DNSDecision.Err` field
5. Request proceeds with potentially stale/incorrect address
6. **No error response sent to response channel**

#### Code Location: dns_policy_manager.go:303-316 (resolveCadence)

Same pattern exists in cadence mode:

```go
addrs, ttl, lookupDur, err := m.lookup(ctx, host)
if err != nil {
    if len(cached.addrs) > 0 && m.policy.fallbackEnabled() {
        addr := net.JoinHostPort(cached.addrs[0].String(), port)
        decision := DNSDecision{
            Host:          host,
            Mode:          m.policy.Mode,
            ResolvedAddrs: cached.addrs,
            ExpiresAt:     cadenceNext,
            Err:           err,  // ← Error recorded but not propagated
        }
        return dnsResolveOutcome{address: addr, decision: &decision}, nil  // ← Returns SUCCESS
    }
    return dnsResolveOutcome{}, err
}
```

### Error Flow Analysis

#### Normal Error Path (Working)
1. DNS/connection error occurs
2. `client.Do()` returns error (`task_http.go:192`)
3. `dnsMgr.observeResult()` called with error (`task_http.go:195`)
4. Error response sent to channel (`task_http.go:197`)
5. Early return with error (`task_http.go:198`)

#### Broken Error Path (Fallback Enabled)
1. DNS lookup fails in `prepareRequest()` → `resolve()`
2. Fallback logic returns `nil` error with cached address
3. `RoundTrip()` proceeds with cached address (`dns_policy_manager.go:495`)
4. Request may succeed with stale address OR fail later at connection level
5. Original DNS error never reaches response channel

## Affected Components

### Primary Files
- `dns_policy_manager.go`: Contains fallback logic that swallows errors
  - Lines 234-249: `resolveTTL()` fallback path
  - Lines 303-316: `resolveCadence()` fallback path

### Secondary Files
- `task_http.go`: Error handling in `httpRequest.Execute()`
  - Lines 192-198: Normal error path (working correctly)
- `dns_policy.go`: Policy configuration and validation
  - Lines 147-150: `fallbackEnabled()` method

## Impact Assessment

### Critical
- Monitoring systems cannot detect DNS resolution failures
- Failed requests may appear successful if cached address is reused
- Guard rail mechanisms may not trigger appropriately
- Silent failures violate principle of least surprise

### Moderate
- DNS decision hooks receive error information but most clients don't use them
- Fallback behavior may be intentional for resilience, but should be explicit

### Low
- Static mode works correctly (no DNS involved)
- Single lookup mode works correctly for initial lookup failure

## Implementation Options

### Option 1: Always Propagate DNS Errors (Strictest)

**Change:** Remove fallback behavior entirely or make it opt-in via explicit flag

**Pros:**
- Clear error semantics
- Monitoring systems see all failures
- Predictable behavior

**Cons:**
- May break existing resilience assumptions
- Could increase error volume in transient DNS issues
- Breaking change for users relying on fallback

### Option 2: Report DNS Errors While Using Fallback (Balanced)

**Change:** Send error response to channel even when falling back to cached address

**Implementation:**
```go
if err != nil {
    if len(cached.addrs) > 0 && m.policy.fallbackEnabled() {
        // Create decision with error
        decision := DNSDecision{
            Host:          host,
            Mode:          m.policy.Mode,
            ResolvedAddrs: cached.addrs,
            Err:           err,
            FallbackUsed:  true,  // New field
        }
        // Return outcome but preserve error for reporting
        return dnsResolveOutcome{
            address:      addr,
            decision:     &decision,
            shouldReport: true,   // New field to trigger error response
        }, err  // Return error instead of nil
    }
    return dnsResolveOutcome{}, err
}
```

**Pros:**
- Maintains resilience of fallback behavior
- Errors visible to monitoring systems
- Clear indication of degraded state

**Cons:**
- Requires changes to `dnsResolveOutcome` struct
- Needs new logic to send error while continuing request
- More complex error handling

### Option 3: Explicit Fallback Mode with Warnings (Most Flexible)

**Change:** Add `DNSFallbackBehavior` enum to policy configuration

```go
type DNSFallbackBehavior uint8

const (
    DNSFallbackError    DNSFallbackBehavior = iota  // Fail immediately on DNS error
    DNSFallbackWarn                                  // Use cache but send warning response
    DNSFallbackSilent                                // Current behavior (use cache, no error)
)

type DNSPolicy struct {
    // ... existing fields ...
    FallbackBehavior DNSFallbackBehavior  // Replace DisableFallback bool
}
```

**Pros:**
- Maximum flexibility for different use cases
- Non-breaking (default to Silent for backward compat)
- Explicit control over error reporting

**Cons:**
- More configuration complexity
- Requires additional testing scenarios
- Documentation burden

## Recommended Solution

**Option 2** (Report DNS Errors While Using Fallback) provides the best balance:

1. **Preserve resilience**: Fallback to cached addresses still works
2. **Improve observability**: DNS errors always reported to response channel
3. **Clear semantics**: Clients can distinguish between clean success and degraded fallback
4. **Non-breaking**: Existing fallback behavior continues, just with better error visibility

### Implementation Steps

1. **Modify `dnsResolveOutcome` struct** (dns_policy_manager.go):
   ```go
   type dnsResolveOutcome struct {
       address      string
       forceNewConn bool
       decision     *DNSDecision
       fallbackErr  error  // New: DNS error when fallback is used
   }
   ```

2. **Update `resolveTTL()` fallback path** (dns_policy_manager.go:234-249):
   ```go
   if err != nil {
       if len(cached.addrs) > 0 && m.policy.fallbackEnabled() {
           // ... create decision ...
           return dnsResolveOutcome{
               address:     addr,
               decision:    &decision,
               fallbackErr: err,  // Preserve error for reporting
           }, nil
       }
       return dnsResolveOutcome{}, err
   }
   ```

3. **Update `resolveCadence()` fallback path** (dns_policy_manager.go:303-316):
   Same pattern as `resolveTTL()`

4. **Modify `prepareRequest()` to check for fallback errors** (dns_policy_manager.go:82-133):
   ```go
   outcome, err := m.resolve(ctx, host, port)
   if err != nil {
       // ... existing error handling ...
   }

   // Check if fallback was used (new)
   if outcome.fallbackErr != nil && outcome.decision != nil {
       outcome.decision.FallbackUsed = true
       // Return error context for caller to handle
       return ctx, outcome.forceNewConn, outcome.fallbackErr
   }
   ```

5. **Add error handling in `policyTransport.RoundTrip()`** (dns_policy_manager.go:485-496):
   ```go
   func (p *policyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
       ctx, _, err := p.mgr.prepareRequest(req.Context(), req.URL, p.base)
       if err != nil {
           // DNS error occurred (either hard failure or fallback scenario)
           return nil, err
       }

       newReq := req.Clone(ctx)
       return p.base.RoundTrip(newReq)
   }
   ```

6. **Add `FallbackUsed` field to `DNSDecision`** (dns_policy.go):
   ```go
   type DNSDecision struct {
       // ... existing fields ...
       FallbackUsed bool  // New: indicates cached address used due to DNS error
   }
   ```

7. **Add comprehensive tests**:
   - Test DNS failure with fallback enabled (should send error response)
   - Test DNS failure with fallback disabled (should send error response)
   - Test DNS failure without cached address (should send error response)
   - Verify decision hook receives correct fallback state

### Testing Strategy

1. **Unit tests** for each `resolve*()` method:
   - DNS failure with cached address and fallback enabled
   - DNS failure with cached address and fallback disabled
   - DNS failure without cached address

2. **Integration tests** for end-to-end flow:
   - HTTP task execution with DNS failure scenarios
   - Verify error responses sent to response channel
   - Verify guard rail mechanisms trigger appropriately

3. **Regression tests**:
   - Ensure static mode continues to work
   - Ensure default mode continues to work
   - Ensure normal success paths unchanged

## Migration Considerations

### Backward Compatibility
- Existing fallback behavior preserved (requests still use cached addresses)
- New error reporting may increase error response volume
- Clients should be prepared to handle errors that were previously silent

### Documentation Updates
- Update DNS policy documentation to explain fallback error reporting
- Add examples showing how to handle DNS fallback errors
- Document difference between hard DNS failures and fallback scenarios

### Monitoring Impact
- Error dashboards may show increased error counts (this is expected and correct)
- Add metrics to distinguish between:
  - Hard DNS failures (no cached address)
  - Soft DNS failures (fallback to cached address)
  - Guard rail triggers due to DNS issues

## Future Enhancements

1. **Fallback address TTL/expiration**: Don't fall back to very stale cached addresses
2. **Fallback metrics**: Track how often fallback is used per endpoint
3. **Configurable fallback policy**: Per-endpoint control over fallback behavior
4. **DNS error classification**: Distinguish temporary vs permanent DNS failures
5. **Circuit breaker integration**: Use DNS error patterns to trigger circuit breakers

## References

- Issue discovered: DNS resolution failures not producing error responses
- Affected files: `dns_policy_manager.go`, `task_http.go`, `dns_policy.go`
- Related guard rail mechanism: `DNSGuardRailPolicy` in `dns_policy.go`
