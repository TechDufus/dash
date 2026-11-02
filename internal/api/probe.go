package api

import (
	"log/slog"
	"strings"
	"sync"

	"github.com/TechDufus/dash/internal/auth"
)

const (
	// maxProbeNames bounds the names the probe remembers; after that it is silent.
	maxProbeNames = 256
	// maxProbeUser caps a logged and remembered name (the header comes from the client).
	maxProbeUser = 128
)

// principalProbe logs the first sighting of each distinct user name in the process, so the
// operator can read from the log what identity the proxy in front of dash really sends (which
// user, from which TCP peer, in which header) before a release starts to depend on it. It only
// observes: the request is not changed or refused, and no header value other than the resolved
// name is logged.
type principalProbe struct {
	seen sync.Map     // name -> struct{}: the lock-free path for a name already logged
	mu   sync.Mutex   // guards n and the decision to log a new name
	n    int          // names logged; never above maxProbeNames
	log  *slog.Logger // nil = slog.Default(); tests set it before the first request
}

// see logs p once per distinct user name, up to maxProbeNames names. peer is the TCP peer's
// host; p.Via is what carried the identity.
func (pr *principalProbe) see(p auth.Principal, peer string) {
	name := p.User
	if len(name) > maxProbeUser {
		name = strings.ToValidUTF8(name[:maxProbeUser], "")
	}
	if _, ok := pr.seen.Load(name); ok {
		return
	}
	pr.mu.Lock()
	if _, ok := pr.seen.Load(name); ok || pr.n >= maxProbeNames {
		pr.mu.Unlock()
		return
	}
	pr.n++
	// A copy: name may be a substring of the client's header value, which the map would keep alive.
	pr.seen.Store(strings.Clone(name), struct{}{})
	pr.mu.Unlock()

	l := pr.log
	if l == nil {
		l = slog.Default()
	}
	l.Info("principal seen", "user", name, "kind", string(p.Kind), "role", string(p.Role), "peer", peer, "via", p.Via)
}
