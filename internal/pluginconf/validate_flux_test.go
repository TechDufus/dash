package pluginconf

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

// The tags are the file keys and the environment names, so they are pinned.
func TestFluxSectionKeys(t *testing.T) {
	for _, tc := range []struct {
		typ   reflect.Type
		field string
		json  string
	}{
		{reflect.TypeFor[Flux](), "SuspendReasonAnnotation", "suspendReasonAnnotation"},
		{reflect.TypeFor[Flux](), "Verify", "verify"},
		{reflect.TypeFor[Flux](), "Actions", "actions"},
		{reflect.TypeFor[FluxVerify](), "RequireWhenSpecVerify", "requireWhenSpecVerify"},
		{reflect.TypeFor[FluxVerify](), "Sources", "sources"},
		{reflect.TypeFor[VerifySource](), "Kind", "kind"},
		{reflect.TypeFor[VerifySource](), "Namespace", "namespace"},
		{reflect.TypeFor[VerifySource](), "Name", "name"},
	} {
		f, ok := tc.typ.FieldByName(tc.field)
		if !ok {
			t.Errorf("%s has no %s", tc.typ, tc.field)
			continue
		}
		if got := f.Tag.Get("json"); got != tc.json {
			t.Errorf("%s.%s json tag = %q, want %q", tc.typ, tc.field, got, tc.json)
		}
		if f.Tag.Get("doc") == "" {
			t.Errorf("%s.%s has no doc tag", tc.typ, tc.field)
		}
	}
	// The setting that existed before the plugin keeps its text, so the generated documentation
	// does not change under an operator.
	f, _ := reflect.TypeFor[Flux]().FieldByName("SuspendReasonAnnotation")
	const doc = "Flux object annotation whose value is shown as the reason the object is suspended. Empty hides the reason."
	if got := f.Tag.Get("doc"); got != doc {
		t.Errorf("doc tag = %q", got)
	}
	if got := f.Tag.Get("dash"); got != "" {
		t.Errorf("dash tag = %q, want none", got)
	}
}

func TestFluxDefaultsEnforceSpecVerifyAndPinNothing(t *testing.T) {
	d := DefaultFlux()
	if !d.Verify.RequireWhenSpecVerify || len(d.Verify.Sources) != 0 || d.Actions.Enabled || d.SuspendReasonAnnotation != "" {
		t.Errorf("DefaultFlux = %+v", d)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("the defaults are invalid: %v", err)
	}
	if z := (Flux{}); z.Verify.RequireWhenSpecVerify {
		t.Error("a zero Flux requires spec.verify: only DefaultFlux (the config layer) turns it on")
	}
}

func TestFluxVerifySourcesValidation(t *testing.T) {
	pin := func(kind, ns, name string) VerifySource { return VerifySource{Kind: kind, Namespace: ns, Name: name} }
	for _, tc := range []struct {
		name string
		in   []VerifySource
		want []string
	}{
		{"none", nil, nil},
		{"a git repository", []VerifySource{pin("GitRepository", "gitops-system", "gitops-system")}, nil},
		{"an oci repository", []VerifySource{pin("OCIRepository", "gitops-system", "charts")}, nil},
		{"both kinds with one name", []VerifySource{pin("GitRepository", "a", "x"), pin("OCIRepository", "a", "x")}, nil},
		{"a kustomization", []VerifySource{pin("Kustomization", "a", "x")}, []string{"verify.sources"}},
		{"a helm repository", []VerifySource{pin("HelmRepository", "a", "x")}, []string{"verify.sources"}},
		{"no kind", []VerifySource{pin("", "a", "x")}, []string{"verify.sources"}},
		{"a kind in the wrong case", []VerifySource{pin("gitrepository", "a", "x")}, []string{"verify.sources"}},
		{"no namespace", []VerifySource{pin("GitRepository", "", "x")}, []string{"verify.sources"}},
		{"no name", []VerifySource{pin("GitRepository", "a", "")}, []string{"verify.sources"}},
		{"a duplicate", []VerifySource{pin("GitRepository", "a", "x"), pin("GitRepository", "a", "x")}, []string{"verify.sources"}},
		{"two problems", []VerifySource{pin("Nope", "a", "x"), pin("GitRepository", "", "x")}, []string{"verify.sources", "verify.sources"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Flux{Verify: FluxVerify{Sources: tc.in}}.Validate()
			if got := paths(err); !slices.Equal(got, tc.want) {
				t.Fatalf("paths = %v, want %v (%v)", got, tc.want, err)
			}
		})
	}
}

func TestFluxSectionRoundTripsThroughJSON(t *testing.T) {
	var f Flux
	in := `{"enabled":true,"suspendReasonAnnotation":"example.com/suspended",` +
		`"verify":{"requireWhenSpecVerify":false,"sources":[{"kind":"GitRepository","namespace":"a","name":"b"}]},` +
		`"actions":{"enabled":true,"denyNamespaces":["kube-system"]}}`
	if err := json.Unmarshal([]byte(in), &f); err != nil {
		t.Fatal(err)
	}
	want := VerifySource{Kind: "GitRepository", Namespace: "a", Name: "b"}
	if f.SuspendReasonAnnotation != "example.com/suspended" || f.Enabled == nil || !*f.Enabled ||
		f.Verify.RequireWhenSpecVerify || !slices.Equal(f.Verify.Sources, []VerifySource{want}) ||
		!f.Actions.Enabled || !slices.Equal(f.Actions.DenyNamespaces, []string{"kube-system"}) {
		t.Errorf("flux = %+v", f)
	}
}

// The action policy hook is what lets the supervisor mount reconcile and the core apply the deny
// list to its target.
func TestFluxActionsConfigIsTheSection(t *testing.T) {
	f := Flux{Actions: Actions{Enabled: true, DenyNamespaces: []string{"kube-system"}}}
	got := f.ActionsConfig()
	if !got.Enabled || !slices.Equal(got.DenyNamespaces, []string{"kube-system"}) {
		t.Errorf("ActionsConfig = %+v", got)
	}
	var v any = &Flux{}
	if _, ok := v.(interface{ ReadDenyConfig() []string }); ok {
		t.Error("*Flux has a read deny list: the plugin has no read route")
	}
}
