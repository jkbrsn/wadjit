package wadjit

import (
	"context"
	"errors"
	"net/netip"
	"time"
)

var (
	// ErrInvalidDNSPolicy indicates the DNSPolicy configuration is invalid.
	ErrInvalidDNSPolicy = errors.New("invalid DNS policy configuration")
)

// DNSRefreshMode controls how an endpoint refreshes its DNS resolution and TCP/TLS connections.
type DNSRefreshMode uint8

const (
	// DNSRefreshDefault mirrors the current Go http.Transport behaviour without extra policies.
	DNSRefreshDefault DNSRefreshMode = iota
	// DNSRefreshStatic keeps using a literal address, bypassing DNS entirely.
	DNSRefreshStatic
	// DNSRefreshSingleLookup performs a single DNS lookup during Initialize and reuses it indefinitely.
	DNSRefreshSingleLookup
	// DNSRefreshTTL refreshes DNS when the observed TTL expires.
	DNSRefreshTTL
	// DNSRefreshCadence refreshes DNS on a fixed cadence supplied by the user, regardless of TTL.
	DNSRefreshCadence
)

// GuardRailAction describes how to react when guard rail conditions are met.
type GuardRailAction uint8

const (
	// GuardRailActionNone leaves connections untouched when guard rail triggers.
	GuardRailActionNone GuardRailAction = iota
	// GuardRailActionFlush drops idle connections so the next request dials afresh.
	GuardRailActionFlush
	// GuardRailActionForceLookup flushes and also forces a fresh DNS lookup on next use.
	GuardRailActionForceLookup
)

// GuardRailPolicy configures optional safety overrides that activate on repeated errors.
type GuardRailPolicy struct {
	// ConsecutiveErrorThreshold triggers the guard rail after this many sequential failures. Zero disables guard rails.
	ConsecutiveErrorThreshold int
	// Window is the rolling time window used for counting errors. Zero means no windowing (errors counted indefinitely).
	Window time.Duration
	// Action determines what happens once the threshold is hit.
	Action GuardRailAction
}

// Enabled reports whether the guard rail is active.
func (p GuardRailPolicy) Enabled() bool {
	return p.ConsecutiveErrorThreshold > 0 && p.Action != GuardRailActionNone
}

// DNSPolicy encapsulates the DNS refresh behaviour for an endpoint.
type DNSPolicy struct {
	Mode DNSRefreshMode

	// StaticAddr is used when Mode is DNSRefreshStatic or as a fallback for SingleLookup.
	StaticAddr netip.AddrPort

	// Cadence defines how often reconnects happen in cadence mode.
	Cadence time.Duration

	// TTLMin and TTLMax clamp the observed TTL when using DNSRefreshTTL.
	TTLMin time.Duration
	TTLMax time.Duration

	// AllowFallback retains the last known address if a refresh fails.
	AllowFallback bool

	// GuardRail configures optional guard rail behaviour.
	GuardRail GuardRailPolicy

	// Resolver optionally overrides the default TTL resolver implementation.
	Resolver TTLResolver
}

// TTLResolver looks up host records and returns associated TTL information.
type TTLResolver interface {
	Lookup(ctx context.Context, host string) ([]netip.Addr, time.Duration, error)
}

// DNSDecisionCallback records decisions made by the policy engine.
type DNSDecisionCallback func(ctx context.Context, decision DNSDecision)

// DNSDecision captures metadata about a DNS refresh decision.
type DNSDecision struct {
	Host               string
	Mode               DNSRefreshMode
	ResolvedAddrs      []netip.Addr
	TTL                time.Duration
	ExpiresAt          time.Time
	GuardRailTriggered bool
	Err                error
}

// Validate ensures the DNS policy fields are coherent.
func (p DNSPolicy) Validate() error {
	switch p.Mode {
	case DNSRefreshDefault:
		// No additional requirements.
	case DNSRefreshStatic:
		if (p.StaticAddr == netip.AddrPort{}) {
			return errors.Join(ErrInvalidDNSPolicy, errors.New("static mode requires StaticAddr"))
		}
	case DNSRefreshSingleLookup:
		if (p.StaticAddr == netip.AddrPort{}) && p.Resolver == nil {
			// We allow relying on the default resolver if StaticAddr is empty.
		}
	case DNSRefreshTTL:
		if p.TTLMax > 0 && p.TTLMin > p.TTLMax {
			return errors.Join(ErrInvalidDNSPolicy, errors.New("TTLMin must be <= TTLMax"))
		}
	case DNSRefreshCadence:
		if p.Cadence <= 0 {
			return errors.Join(ErrInvalidDNSPolicy, errors.New("cadence mode requires positive Cadence"))
		}
	default:
		return errors.Join(ErrInvalidDNSPolicy, errors.New("unknown DNS refresh mode"))
	}

	return nil
}

// normalize bounds TTL durations ensuring sane defaults and ordering.
func (p DNSPolicy) normalizeTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return p.TTLMin
	}
	if p.TTLMin > 0 && ttl < p.TTLMin {
		return p.TTLMin
	}
	if p.TTLMax > 0 && ttl > p.TTLMax {
		return p.TTLMax
	}
	return ttl
}
