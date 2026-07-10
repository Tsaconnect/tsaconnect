package chains

// Registry holds one ChainClient per (network, chainKey) pair, mirroring how the
// EVM side keeps a full mainnet + testnet config side by side. A chain is only
// registered when that network's config is present, so absent combinations stay
// dormant (ForKey → false).
type Registry struct {
	byKey map[string]ChainClient // key = "<network>:<chainKey>"
}

func NewRegistry() *Registry {
	return &Registry{byKey: map[string]ChainClient{}}
}

func regKey(network, chainKey string) string { return network + ":" + chainKey }

// Register wires a client for a (network, chainKey), e.g. ("mainnet", "tron").
func (r *Registry) Register(network, chainKey string, c ChainClient) {
	r.byKey[regKey(network, chainKey)] = c
}

// ForKey looks up the client for a (network, chainKey); ok is false when that
// combination isn't configured (dormant).
func (r *Registry) ForKey(network, chainKey string) (ChainClient, bool) {
	c, ok := r.byKey[regKey(network, chainKey)]
	return c, ok
}

func (r *Registry) Keys() []string {
	out := make([]string, 0, len(r.byKey))
	for k := range r.byKey {
		out = append(out, k)
	}
	return out
}
