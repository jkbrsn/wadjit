package wadjit

// WithDNSPolicy attaches a DNS policy to the HTTP endpoint.
func WithDNSPolicy(policy DNSPolicy) HTTPEndpointOption {
	return func(ep *HTTPEndpoint) {
		ep.dnsPolicy = policy
	}
}

// WithDNSDecisionHook registers a callback invoked for each DNS decision.
func WithDNSDecisionHook(cb DNSDecisionCallback) HTTPEndpointOption {
	return func(ep *HTTPEndpoint) {
		ep.dnsDecisionHook = cb
	}
}
