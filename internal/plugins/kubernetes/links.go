package kubernetes

import (
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// linksOf is the links of one object into the tools whose plugins run: never nil.
func linksOf(l plugin.LinkResolver, kind, namespace, name string) []model.Link {
	if l == nil {
		return []model.Link{}
	}
	return l.Resolve(plugin.LinkRequest{Kind: kind, Namespace: namespace, Name: name})
}
