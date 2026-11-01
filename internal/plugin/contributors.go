package plugin

// Contributor is a running instance and the overview sources it implements; each is nil when the
// plugin does not implement it.
type Contributor struct {
	ID        ID
	Attention AttentionSource
	KPIs      KPISource
	Topology  TopologySource
}

// Contributors lists, in registry order, the running instances that implement at least one of
// AttentionSource, KPISource and TopologySource. The core composes core.overview from them
// (design A.7); an instance that is not running contributes nothing.
func (s *Supervisor) Contributors() []Contributor {
	var out []Contributor
	for _, in := range s.registry() {
		in.mu.RLock()
		running, p := in.running, in.p
		in.mu.RUnlock()
		if !running || p == nil {
			continue
		}
		c := Contributor{ID: in.id}
		c.Attention, _ = p.(AttentionSource)
		c.KPIs, _ = p.(KPISource)
		c.Topology, _ = p.(TopologySource)
		if c.Attention != nil || c.KPIs != nil || c.Topology != nil {
			out = append(out, c)
		}
	}
	return out
}
