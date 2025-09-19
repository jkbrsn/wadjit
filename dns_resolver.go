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

// newDefaultTTLResolver builds a resolver that can report record TTLs using the system configuration.
func newDefaultTTLResolver() *defaultTTLResolver {
	return &defaultTTLResolver{client: &dns.Client{}, stdResolver: net.DefaultResolver}
}

// Lookup performs DNS lookups for A and AAAA records, returning the combined addresses and the
// minimum TTL among answers. Falls back to the standard resolver if TTLs cannot be determined.
func (r *defaultTTLResolver) Lookup(ctx context.Context, host string) ([]netip.Addr, time.Duration, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, 0, nil
	}

	host = strings.TrimSuffix(host, ".")
	addrs, ttl, err := r.lookupWithTTL(ctx, host)
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
func (r *defaultTTLResolver) lookupWithTTL(ctx context.Context, host string) ([]netip.Addr, time.Duration, error) {
	r.once.Do(func() {
		r.cfg, r.cfgErr = dns.ClientConfigFromFile("/etc/resolv.conf")
		if r.cfgErr == nil {
			r.client.Timeout = 5 * time.Second
		}
	})

	if r.cfgErr != nil {
		return nil, 0, r.cfgErr
	}

	var (
		ttl     time.Duration
		addrs   []netip.Addr
		ttlInit bool
	)

	queryTypes := []uint16{dns.TypeA, dns.TypeAAAA}
	for _, qtype := range queryTypes {
		msg := dns.Msg{}
		msg.SetQuestion(dns.Fqdn(host), qtype)

		for _, server := range r.cfg.Servers {
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			default:
			}

			target := net.JoinHostPort(server, r.cfg.Port)
			resp, _, err := r.client.ExchangeContext(ctx, &msg, target)
			if err != nil {
				// try next server
				continue
			}
			if resp == nil || resp.Rcode != dns.RcodeSuccess {
				continue
			}

			for _, ans := range resp.Answer {
				header := ans.Header()
				if header == nil {
					continue
				}
				ttlDur := time.Duration(header.Ttl) * time.Second
				switch rr := ans.(type) {
				case *dns.A:
					ip, ok := netip.AddrFromSlice(rr.A)
					if !ok {
						continue
					}
					addrs = append(addrs, ip)
				case *dns.AAAA:
					ip, ok := netip.AddrFromSlice(rr.AAAA)
					if !ok {
						continue
					}
					addrs = append(addrs, ip)
				default:
					continue
				}

				if !ttlInit || (ttlDur > 0 && ttlDur < ttl) {
					ttl = ttlDur
					ttlInit = true
				}
			}

			if len(addrs) > 0 {
				return addrs, ttl, nil
			}
		}
	}

	if len(addrs) == 0 {
		return nil, 0, errors.New("no DNS answers with TTL")
	}
	return addrs, ttl, nil
}
