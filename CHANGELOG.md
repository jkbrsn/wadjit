# Changelog

Notable changes to this project will be documented in this file. To keep it lightweight, releases 2+ minor versions back will be churned regularly.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## [0.10.x] - Unreleased

### Changed

#### DNS Errors Now Reported With Fallback (Breaking Change)

Previously, when DNS resolution failed but a cached address was available for fallback, the error was silently swallowed and `WatcherResponse.Err` would be `nil`. This made it impossible to detect DNS degradation in monitoring systems.

**Now**, DNS errors are always reported in `WatcherResponse.Err`, even when fallback succeeds. This provides visibility into DNS issues while maintaining resilience.

**Before:**
```go
// DNS fails but fallback uses cached address → resp.Err == nil
resp := <-wadjit.Responses()
if resp.Err != nil {
    // Only triggered for hard failures
    log.Error("Request failed", resp.Err)
}
```

**After:**
```go
// DNS fails with fallback → resp.Err != nil (contains DNS error)
resp := <-wadjit.Responses()
if resp.Err != nil {
    // This now triggers for DNS failures too!
    // But request may have completed using cached address
    log.Error("Request failed", resp.Err)
}
```

### Added

#### New DNS Metadata Fields

Two new fields added to `DNSMetadata` and `DNSDecision`:

- `FallbackUsed bool`: Indicates whether a cached address was used due to DNS lookup failure
- `Err error`: Contains the DNS resolution error, if any occurred

**Access DNS error information:**
```go
resp := <-wadjit.Responses()
md := resp.Metadata()
if md.DNS != nil {
    if md.DNS.FallbackUsed {
        log.Warn("Using cached DNS entry", "error", md.DNS.Err)
    }
    if md.DNS.Err != nil {
        log.Warn("DNS error occurred", "error", md.DNS.Err)
    }
}
```

#### Enhanced DNS Decision Callbacks

DNS decision hooks now receive fallback and error information:

```go
policy := wadjit.DNSPolicy{
    Mode: wadjit.DNSRefreshTTL,
    TTLMin: time.Minute,
    DecisionCallback: func(ctx context.Context, decision wadjit.DNSDecision) {
        if decision.FallbackUsed {
            log.Warn("DNS fallback triggered", "error", decision.Err)
        }
        if decision.Err != nil {
            metrics.RecordDNSError(decision.Host, decision.Err)
        }
    },
}
```

### Migration Guide

#### 1. Distinguish Between Hard Failures and Fallback Scenarios

Update error handling to check if fallback was used:

```go
resp := <-wadjit.Responses()
if resp.Err != nil {
    md := resp.Metadata()
    if md.DNS != nil && md.DNS.FallbackUsed {
        // DNS failed but request completed with cached address
        log.Warn("DNS fallback used",
            "error", md.DNS.Err,
            "cached_addr", md.DNS.ResolvedAddrs)
        // Maybe alert but don't fail completely
    } else {
        // Hard failure - request did not complete
        log.Error("Request failed completely", resp.Err)
        // Definitely alert/retry
    }
}
```

#### 2. Update Monitoring and Alerting

Expect increased error counts in dashboards - this is correct behavior. DNS failures were always happening, just not visible before.

Distinguish between error types in metrics:

```go
resp := <-wadjit.Responses()
if resp.Err != nil {
    md := resp.Metadata()
    if md.DNS != nil {
        if md.DNS.FallbackUsed {
            metrics.IncrCounter("dns.fallback.used")    // Degraded but working
            metrics.IncrCounter("dns.errors.soft")
        } else if md.DNS.Err != nil {
            metrics.IncrCounter("dns.errors.hard")      // Complete failure
        }
    }
    metrics.IncrCounter("requests.errors.total")
}
```

#### 3. Refine Retry Logic

Consider different retry strategies for fallback vs hard failures:

```go
func shouldRetry(resp WatcherResponse) bool {
    if resp.Err == nil {
        return false // Success
    }

    md := resp.Metadata()
    if md.DNS != nil && md.DNS.FallbackUsed {
        // DNS failed but got cached data - maybe don't retry immediately
        // as the DNS issue might be temporary
        return false
    }

    // Hard failure or non-DNS error - retry
    return true
}
```

#### 4. Update Error Propagation Logic

If code assumed `Err == nil` means complete success:

```go
// OLD - May need updating
if resp.Err != nil {
    return fmt.Errorf("health check failed: %w", resp.Err)
}

// NEW - More nuanced
if resp.Err != nil {
    md := resp.Metadata()
    if md.DNS != nil && md.DNS.FallbackUsed {
        // Degrade gracefully - log warning but continue
        metrics.IncrDNSFallback(resp.URL.Host)
    } else {
        // Hard failure - propagate error
        return fmt.Errorf("health check failed: %w", resp.Err)
    }
}
```

### Recommended Migration Steps

1. **Update error handling** to check for `DNS.FallbackUsed` before treating errors as hard failures
2. **Update monitoring** to distinguish soft (fallback) vs hard DNS failures
3. **Review retry logic** - consider not retrying immediately on fallback errors
4. **Test in staging** - watch for increased error counts (expected and correct)
5. **Update runbooks** - DNS fallback errors may now trigger alerts that need different responses

### Technical Details

- Modified `dnsResolveOutcome` to include `fallbackErr` field for error propagation
- Updated `resolveTTL()`, `resolveCadence()`, and `resolveSingle()` to return DNS decisions even on error
- Added `lastDecision` field to `dnsPolicyManager` for error reporting in HTTP responses
- Created `httpTaskResponseError` type to attach DNS metadata to error responses
- Enhanced decision context propagation to include errors and fallback state
