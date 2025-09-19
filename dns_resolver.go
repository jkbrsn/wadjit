package wadjit

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// defaultTTLResolver implements TTLResolver using the host system resolver configuration.
type defaultTTLResolver struct {
	once   sync.Once
	cfg    *dns.ClientConfig
	cfgErr error

	client *dns.Client

	stdResolver *net.Resolver
}

var errNoTTLAnswer = errors.New("no DNS answers with TTL")

// newDefaultTTLResolver builds a resolver that can report record TTLs using the system DNS
// settings.
func newDefaultTTLResolver() *defaultTTLResolver {
	return &defaultTTLResolver{client: &dns.Client{}, stdResolver: net.DefaultResolver}
}

// Lookup performs DNS lookups for A and AAAA records, returning the combined addresses and the
// minimum TTL among answers. Falls back to the standard resolver if TTLs cannot be determined.
func (r *defaultTTLResolver) Lookup(
	ctx context.Context,
	host string,
) ([]netip.Addr, time.Duration, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, 0, nil
	}

	trimmedHost := strings.TrimSuffix(host, ".")
	addrs, ttl, err := r.lookupWithTTL(ctx, trimmedHost)
	if err == nil && len(addrs) > 0 {
		return addrs, ttl, nil
	}

	// fallback to standard resolver without TTL.
	ips, fallbackErr := r.stdResolver.LookupNetIP(ctx, "ip", host)
	if fallbackErr != nil {
		if err != nil {
			return nil, 0, errors.Join(err, fallbackErr)
		}
		return nil, 0, fallbackErr
	}

	// Convert []net.IP to []netip.Addr
	var addrList []netip.Addr
	addrList = append(addrList, ips...)
	return addrList, 0, nil
}

// lookupWithTTL performs raw DNS queries to extract addresses and the minimum TTL when available.
func (r *defaultTTLResolver) lookupWithTTL(
	ctx context.Context,
	host string,
) ([]netip.Addr, time.Duration, error) {
	if err := r.initConfig(); err != nil {
		return nil, 0, err
	}

	var (
		collected []netip.Addr
		minTTL    time.Duration
		seenTTL   bool
	)

	for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA} {
		addrs, ttl, err := r.queryType(ctx, host, qtype)
		if err != nil {
			if errors.Is(err, errNoTTLAnswer) {
				continue
			}
			return nil, 0, err
		}
		if len(addrs) == 0 {
			continue
		}
		collected = append(collected, addrs...)
		if ttl > 0 && (!seenTTL || ttl < minTTL) {
			minTTL = ttl
			seenTTL = true
		}
	}

	if len(collected) == 0 {
		return nil, 0, errNoTTLAnswer
	}

	return collected, minTTL, nil
}

// initConfig lazily loads resolver configuration and ensures the DNS client is
// ready for queries.
func (r *defaultTTLResolver) initConfig() error {
	r.once.Do(func() {
		r.cfg, r.cfgErr = dns.ClientConfigFromFile("/etc/resolv.conf")
		if r.cfgErr == nil {
			r.client.Timeout = 5 * time.Second
		}
	})
	return r.cfgErr
}

// queryType performs a single DNS query for the provided record type and returns
// the resulting addresses alongside the minimum TTL among responses.
func (r *defaultTTLResolver) queryType(
	ctx context.Context,
	host string,
	qtype uint16,
) ([]netip.Addr, time.Duration, error) {
	msg := dns.Msg{}
	msg.SetQuestion(dns.Fqdn(host), qtype)

	for _, server := range r.cfg.Servers {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}

		addrs, ttl, err := r.exchange(ctx, &msg, server)
		if err != nil {
			if errors.Is(err, errNoTTLAnswer) {
				continue
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, 0, err
			}
			continue
		}
		if len(addrs) > 0 {
			return addrs, ttl, nil
		}
	}

	return nil, 0, errNoTTLAnswer
}

// exchange sends the prepared DNS message to the given server, returning parsed
// answers and their minimum TTL.
func (r *defaultTTLResolver) exchange(
	ctx context.Context,
	msg *dns.Msg,
	server string,
) ([]netip.Addr, time.Duration, error) {
	target := net.JoinHostPort(server, r.cfg.Port)
	resp, _, err := r.client.ExchangeContext(ctx, msg, target)
	if err != nil {
		return nil, 0, err
	}
	if resp == nil || resp.Rcode != dns.RcodeSuccess {
		return nil, 0, errNoTTLAnswer
	}

	return parseAnswers(resp.Answer)
}

// parseAnswers extracts IP addresses from DNS resource records and finds the
// lowest TTL value that accompanies them.
func parseAnswers(rrs []dns.RR) ([]netip.Addr, time.Duration, error) {
	var (
		addresses []netip.Addr
		minTTL    time.Duration
		seenTTL   bool
	)

	for _, rr := range rrs {
		header := rr.Header()
		if header == nil {
			continue
		}

		addr, ok := extractAddr(rr)
		if !ok {
			continue
		}

		addresses = append(addresses, addr)
		ttl := time.Duration(header.Ttl) * time.Second
		if ttl > 0 && (!seenTTL || ttl < minTTL) {
			minTTL = ttl
			seenTTL = true
		}
	}

	if len(addresses) == 0 {
		return nil, 0, errNoTTLAnswer
	}

	return addresses, minTTL, nil
}

// extractAddr attempts to convert a DNS answer record into an IP address.
func extractAddr(rr dns.RR) (netip.Addr, bool) {
	switch value := rr.(type) {
	case *dns.A:
		return netip.AddrFromSlice(value.A)
	case *dns.AAAA:
		return netip.AddrFromSlice(value.AAAA)
	default:
		return netip.Addr{}, false
	}
}
