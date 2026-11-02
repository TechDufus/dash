package kubernetes

import (
	"reflect"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// lkResolver records what it is asked and answers with links.
type lkResolver struct {
	asked []plugin.LinkRequest
	links []model.Link
}

func (r *lkResolver) Resolve(req plugin.LinkRequest) []model.Link {
	r.asked = append(r.asked, req)
	return r.links
}

func TestLinksOfAsksTheResolverForTheObject(t *testing.T) {
	want := []model.Link{{Label: "Headlamp", Href: "https://hl.example.com/pod", Kind: "headlamp"}}
	r := &lkResolver{links: want}
	got := linksOf(r, "Pod", "media", "app-0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("links = %+v, want the resolver's %+v", got, want)
	}
	if !reflect.DeepEqual(r.asked, []plugin.LinkRequest{{Kind: "Pod", Namespace: "media", Name: "app-0"}}) {
		t.Errorf("asked = %+v", r.asked)
	}
}

func TestLinksOfWithoutAResolverIsEmptyNotNil(t *testing.T) {
	got := linksOf(nil, "Node", "", "cp-1")
	if got == nil || len(got) != 0 {
		t.Errorf("links = %#v, want an empty non-nil slice", got)
	}
	// A nil resolver of a concrete type is a non-nil interface value in the caller; the plugin
	// never builds one, so only the untyped nil is the "no providers" case.
	var in clusterInput
	if l := linksOf(in.links, "Pod", "a", "b"); l == nil || len(l) != 0 {
		t.Errorf("links of a zero clusterInput = %#v, want an empty non-nil slice", l)
	}
}
