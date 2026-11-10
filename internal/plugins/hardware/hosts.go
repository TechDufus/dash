package hardware

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// hostID is who one configured host is at a moment: its series label, its Proxmox node and its
// name. It is resolved from the settings and the proxmox plugin's services on every poll, series
// call and payload, never cached, so a node the proxmox plugin learns later applies at once.
type hostID struct {
	i        int    // the host's index in plugins.hardware.hosts
	nodename string // the nodename label of its series; "" while unknown
	node     string // the Proxmox node it is (HostData.Node)
	name     string // HostData.Name, and the id of its refs, attention ids and route
	// dupOf is the index of an earlier host that resolves to the same nodename or name, -1 when
	// none does. Such a host is not polled: its series would be the earlier host's, and its refs
	// and ids would collide with them.
	dupOf int
}

// known reports whether the host's nodename is known.
func (id hostID) known() bool { return id.nodename != "" }

// runnable reports whether the host can be polled, given a querier.
func (id hostID) runnable() bool { return id.known() && id.dupOf < 0 }

// route is the host's page.
func (id hostID) route() string { return hostRoute(id.name) }

func hostRoute(name string) string { return routeHardware + "/" + url.PathEscape(name) }

// proxmoxHost reports whether host i is the one the proxmox plugin names: the only host, or the
// one without a nodename (Validate allows one at most).
func (rt *runtime) proxmoxHost(i int) bool {
	return len(rt.cfg.Hosts) == 1 || rt.cfg.Hosts[i].Nodename == ""
}

// hostIDs resolves every configured host, in configuration order. For a host h:
//   - its nodename is h.nodename, else the node the proxmox plugin names; while that plugin names
//     none (it restarts) the host without a nodename keeps the one it last had, so it does not
//     drop out of the payload;
//   - its name is h.name, else its nodename, so it does not move when the Proxmox cluster is read
//     or restarts (refs, attention ids and routes carry it);
//   - its node is the cluster's node named h.nodename (or its first DNS label: pve1.lan is pve1),
//     else the node the proxmox plugin names when h is the host that plugin names, else its
//     nodename. While the cluster's nodes are unknown (before the proxmox plugin's first cluster
//     poll, or after it restarts) a host keeps the node it last resolved against them, so its
//     facts and links do not flap.
func (rt *runtime) hostIDs() []hostID {
	pxNode, pxOK := rt.proxmoxNode()
	clusterNodes, cnOK := rt.clusterNodes()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.lastNode == nil {
		rt.lastNode, rt.lastNodename = map[int]string{}, map[int]string{}
	}
	out := make([]hostID, len(rt.cfg.Hosts))
	for i, h := range rt.cfg.Hosts {
		id := hostID{i: i, nodename: h.Nodename, dupOf: -1}
		if id.nodename == "" {
			if pxOK {
				rt.lastNodename[i] = pxNode
			}
			id.nodename = rt.lastNodename[i]
		}
		label, _, _ := strings.Cut(h.Nodename, ".")
		switch {
		case !cnOK && rt.lastNode[i] != "":
			id.node = rt.lastNode[i]
		case h.Nodename != "" && containsFold(clusterNodes, h.Nodename) != "":
			id.node = containsFold(clusterNodes, h.Nodename)
		case label != "" && containsFold(clusterNodes, label) != "":
			id.node = containsFold(clusterNodes, label)
		case pxOK && rt.proxmoxHost(i):
			id.node = pxNode
		default:
			id.node = id.nodename
		}
		if cnOK && id.node != "" {
			rt.lastNode[i] = id.node
		}
		id.name = h.Name
		if id.name == "" {
			id.name = id.nodename
		}
		if id.known() {
			for _, e := range out[:i] {
				if e.known() && e.dupOf < 0 && (strings.EqualFold(e.nodename, id.nodename) || strings.EqualFold(e.name, id.name)) {
					id.dupOf = e.i
					break
				}
			}
		}
		out[i] = id
	}
	return out
}

// containsFold returns the element of list equal to s ignoring case, or "".
func containsFold(list []string, s string) string {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return x
		}
	}
	return ""
}

// dupMessage says which host is not polled because an earlier one resolves to the same nodename
// or name: two hosts never merge silently.
func (rt *runtime) dupMessage(ids []hostID) string {
	var parts []string
	for _, id := range ids {
		if id.known() && id.dupOf >= 0 {
			parts = append(parts, fmt.Sprintf("hosts entry %d (%s) resolves to the nodename or name of entry %d and is not read", id.i, id.name, id.dupOf))
		}
	}
	return strings.Join(parts, "; ")
}

// nodenameRegex is a PromQL regex literal that matches exactly the given label values.
func nodenameRegex(values []string) string {
	q := make([]string, len(values))
	for i, v := range values {
		q[i] = regexp.QuoteMeta(v)
	}
	return quote("^(" + strings.Join(q, "|") + ")$")
}
