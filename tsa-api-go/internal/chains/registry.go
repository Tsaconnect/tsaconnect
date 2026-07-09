package chains

type Registry struct {
	byKey    map[string]ChainClient
	byFamily map[string]ChainClient
}

func NewRegistry() *Registry {
	return &Registry{byKey: map[string]ChainClient{}, byFamily: map[string]ChainClient{}}
}

// Register wires a client for a chain key (e.g. "tron"). Only called when that
// chain's config is present, so an absent chain stays dormant (ForKey → false).
func (r *Registry) Register(chainKey string, c ChainClient) {
	r.byKey[chainKey] = c
	r.byFamily[c.Family()] = c
}

func (r *Registry) ForKey(chainKey string) (ChainClient, bool) {
	c, ok := r.byKey[chainKey]
	return c, ok
}

func (r *Registry) ForFamily(family string) (ChainClient, bool) {
	c, ok := r.byFamily[family]
	return c, ok
}

func (r *Registry) Keys() []string {
	out := make([]string, 0, len(r.byKey))
	for k := range r.byKey {
		out = append(out, k)
	}
	return out
}
