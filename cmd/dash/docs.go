package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/jsonschema"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// docsUsage is not in the usage text of unknown commands: the command writes this repository's
// docs and is of no use to an operator.
const docsUsage = "dash docs plugins DIR"

// pluginProse is the hand-written part of each page, plugindocs/<id>.md: the text before its first
// "## " heading follows the plugin's description; a "## " section whose heading is one of the
// generated ones is appended to it, and any other comes after them, before "Demo twin".
//
//go:embed plugindocs/*.md
var pluginProse embed.FS

// docsCommand runs `dash docs plugins DIR`: it writes DIR/<id>.md for every plugin dash ships and
// DIR/README.md, the index. `make generate` writes docs/plugins with it and `make check` compares.
func docsCommand(args []string) error {
	if len(args) != 2 || args[0] != "plugins" {
		return fmt.Errorf("usage: %s", docsUsage)
	}
	pages, err := pluginDocPages()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(args[1], 0o755); err != nil {
		return err
	}
	for _, p := range pages {
		if err := os.WriteFile(filepath.Join(args[1], p.name), []byte(p.text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// docPage is one generated file of docs/plugins.
type docPage struct{ name, text string }

// docPlugin is one shipped plugin, as the pages describe it.
type docPlugin struct {
	p plugin.Plugin
	m plugin.Manifest
}

// pluginDocPages renders every page, the index last.
func pluginDocPages() ([]docPage, error) {
	var all []docPlugin
	for _, f := range registry(nil, nil) {
		p := f()
		all = append(all, docPlugin{p, p.Manifest()})
	}
	prose, err := readPluginProse(all)
	if err != nil {
		return nil, err
	}
	vars := config.EnvVars()
	pages := make([]docPage, 0, len(all)+1)
	for _, d := range all {
		text, err := pluginPage(d, all, vars, prose[d.m.ID])
		if err != nil {
			return nil, err
		}
		pages = append(pages, docPage{string(d.m.ID) + ".md", text})
	}
	return append(pages, docPage{"README.md", pluginIndex(all)}), nil
}

// readPluginProse parses plugindocs/*.md; a file that names no shipped plugin is an error.
func readPluginProse(all []docPlugin) (map[plugin.ID]prose, error) {
	files, err := fs.Glob(pluginProse, "plugindocs/*.md")
	if err != nil {
		return nil, err
	}
	out := map[plugin.ID]prose{}
	for _, f := range files {
		id := plugin.ID(strings.TrimSuffix(path.Base(f), ".md"))
		if !slices.ContainsFunc(all, func(d docPlugin) bool { return d.m.ID == id }) {
			return nil, fmt.Errorf("docs: %s names no plugin dash ships", f)
		}
		b, err := pluginProse.ReadFile(f)
		if err != nil {
			return nil, err
		}
		pr, err := parseProse(string(b))
		if err != nil {
			return nil, fmt.Errorf("docs: %s: %w", f, err)
		}
		out[id] = pr
	}
	return out, nil
}

// prose is one plugindocs file: its intro and its "## " sections in file order.
type prose struct {
	intro    string
	sections []docSection
}

type docSection struct{ heading, body string }

// generatedHeadings are the "## " sections every page has, in order; a prose section of the same
// name is appended to it.
var generatedHeadings = []string{"What it shows", "Dependencies", "Settings", "Actions", "Kubernetes RBAC", "Demo twin"}

// parseProse splits a plugindocs file at its "## " lines outside fenced code blocks. A heading
// that differs from a generated one only in case or a trailing "s" is an error: it would
// otherwise become a section of its own instead of joining the generated one.
func parseProse(s string) (prose, error) {
	var out prose
	var cur *docSection
	var text strings.Builder
	flush := func() {
		if cur == nil {
			out.intro = strings.TrimSpace(text.String())
		} else {
			cur.body = strings.TrimSpace(text.String())
			out.sections = append(out.sections, *cur)
		}
		text.Reset()
	}
	fence := ""
	for line := range strings.SplitSeq(s, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case fence != "":
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
		case strings.HasPrefix(trimmed, "```"), strings.HasPrefix(trimmed, "~~~"):
			fence = trimmed[:3]
		case strings.HasPrefix(line, "## "):
			h := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			for _, g := range generatedHeadings {
				if h != g && nearHeading(h) == nearHeading(g) {
					return prose{}, fmt.Errorf("section %q: did you mean %q?", h, g)
				}
			}
			flush()
			cur = &docSection{heading: h}
			continue
		}
		text.WriteString(line)
		text.WriteString("\n")
	}
	flush()
	return out, nil
}

// nearHeading is h in lower case without a trailing "s", to catch a misspelled generated heading.
func nearHeading(h string) string { return strings.TrimSuffix(strings.ToLower(h), "s") }

func pluginPage(d docPlugin, all []docPlugin, vars []config.EnvVar, pr prose) (string, error) {
	id := d.m.ID
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- Generated by `dash docs plugins` from the plugin's manifest and settings and from cmd/dash/plugindocs/%s.md; do not edit. Run `make generate`. -->\n\n", id)
	fmt.Fprintf(&b, "# %s\n\nPlugin ID `%s`. %s\n", d.m.Name, id, d.m.Description)
	if pr.intro != "" {
		fmt.Fprintf(&b, "\n%s\n", pr.intro)
	}
	actions, err := actionsDoc(d.m, vars)
	if err != nil {
		return "", fmt.Errorf("docs: %s: %w", id, err)
	}
	secs := []docSection{
		{"What it shows", showsDoc(d)},
		{"Dependencies", depsDoc(d, all)},
		{"Settings", settingsDoc(id, vars)},
		{"Actions", actions},
		{"Kubernetes RBAC", rbacDoc(id)},
	}
	demo := docSection{"Demo twin", demoDoc(d)}
	var extra []docSection
	for _, s := range pr.sections {
		switch i := slices.IndexFunc(secs, func(g docSection) bool { return g.heading == s.heading }); {
		case i >= 0:
			secs[i].body = joinParas(secs[i].body, s.body)
		case s.heading == demo.heading:
			demo.body = joinParas(demo.body, s.body)
		default:
			extra = append(extra, s)
		}
	}
	for _, s := range append(append(secs, extra...), demo) {
		if s.body != "" {
			fmt.Fprintf(&b, "\n## %s\n\n%s\n", s.heading, s.body)
		}
	}
	return b.String(), nil
}

// joinParas joins the non-empty texts as paragraphs.
func joinParas(texts ...string) string {
	return strings.Join(slices.DeleteFunc(texts, func(s string) bool { return s == "" }), "\n\n")
}

// spaPanel is one panel a plugin's web module puts on a page another owns. The web UI declares
// them (web/src/plugins/panels.ts); a web test holds the generated pages to that registry.
type spaPanel struct{ slot, id, title string }

var spaPanels = map[plugin.ID][]spaPanel{
	"alertmanager": {{"overview.kpis", "alerts-firing", "Alerts firing"}},
	"flux":         {{"overview.kpis", "gitops", "GitOps: ready objects and source verification"}},
	"hardware": {
		{"overview.kpis", "power", "Power"},
		{"overview.kpis", "cpu-temp", "CPU temp"},
	},
	"kubernetes": {
		{"overview.kpis", "nodes", "Nodes"},
		{"storage", "pvcs", "Persistent volumes"},
		{"storage", "backups-etcd", "Backup jobs and the etcd database"},
		{"apps.sources", "apps", "The apps behind HTTP routes"},
	},
	"proxmox": {
		{"overview.kpis", "vms", "VMs"},
		{"storage", "proxmox-storage", "Proxmox storage"},
		{"hardware.facts", "proxmox-node", "The Proxmox node: uptime, version, kernel and CPU"},
	},
	"cnpg":     {{"storage", "cnpg", "Postgres clusters (CNPG)"}},
	"rustfs":   {{"storage", "object-storage", "Object storage"}},
	"launcher": {{"apps.sources", "infrastructure", "Infrastructure links"}},
}

// slotPages names the page each panel slot is on.
var slotPages = map[string]string{
	"overview.kpis":  "Overview, KPI row",
	"storage":        "Storage",
	"apps.sources":   "Apps",
	"hardware.facts": "Hardware, host facts",
}

func showsDoc(d docPlugin) string {
	var b strings.Builder
	m := d.m
	b.WriteString("### Pages\n\n")
	if len(m.UI.Nav) == 0 {
		b.WriteString("No page of its own.\n")
	} else {
		b.WriteString("| Page | Path |\n|---|---|\n")
		for _, n := range m.UI.Nav {
			fmt.Fprintf(&b, "| %s | `%s` |\n", n.Title, n.Path)
		}
	}
	if len(m.UI.Generic) > 0 {
		b.WriteString("\nThe web UI draws these views from its topics:\n\n| View | Kind | Topic |\n|---|---|---|\n")
		for _, g := range m.UI.Generic {
			fmt.Fprintf(&b, "| %s | %s | `%s.%s` |\n", g.Title, g.Kind, m.ID, g.Topic)
		}
	}
	if ps := spaPanels[m.ID]; len(ps) > 0 {
		b.WriteString("\n### Panels on other pages\n\n| Page | Panel | Id |\n|---|---|---|\n")
		for _, p := range ps {
			fmt.Fprintf(&b, "| %s (`%s`) | %s | `%s` |\n", slotPages[p.slot], p.slot, p.title, p.id)
		}
	}
	var overview []string
	if _, ok := d.p.(plugin.AttentionSource); ok {
		overview = append(overview, "attention items")
	}
	if _, ok := d.p.(plugin.KPISource); ok {
		overview = append(overview, "KPIs")
	}
	if _, ok := d.p.(plugin.TopologySource); ok {
		overview = append(overview, "the topology")
	}
	if _, ok := d.p.(plugin.LinkProvider); ok {
		overview = append(overview, "deep links on the objects other plugins show")
	}
	if len(overview) > 0 {
		fmt.Fprintf(&b, "\n### Contributions\n\nIt contributes %s.\n", andList(overview))
	}
	if len(m.Topics) > 0 {
		b.WriteString("\n### Topics\n\nPublished on `/api/v1/stream` and `/api/v1/topics/{topic}`:\n\n| Topic | Payload | What |\n|---|---|---|\n")
		for _, t := range m.Topics {
			fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n", plugin.TopicName(m.ID, t), typeName(t.Type), t.Doc)
		}
	}
	if r, ok := d.p.(plugin.Router); ok {
		if routes := plugin.CollectRoutes(r); len(routes) > 0 {
			b.WriteString("\n### API routes\n\n| Route | What |\n|---|---|\n")
			for _, rt := range routes {
				what := rt.Spec.Summary
				if rt.Stream != nil {
					what += " (server-sent events)"
				}
				fmt.Fprintf(&b, "| `GET /api/v1/plugins/%s%s` | %s |\n", m.ID, rt.Pattern, what)
			}
		}
	}
	if len(m.Series) > 0 {
		names := make([]string, len(m.Series))
		for i, s := range m.Series {
			names[i] = "`" + s.Name + "`"
		}
		fmt.Fprintf(&b, "\n### Series\n\nChart series on `/api/v1/series`: %s.\n", strings.Join(names, ", "))
	}
	return strings.TrimSpace(b.String())
}

// typeName is the Go name of a payload type, pointers dereferenced.
func typeName(v any) string {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return "none"
	}
	return t.Name()
}

func depsDoc(d docPlugin, all []docPlugin) string {
	var lines []string
	for _, id := range d.m.Requires {
		lines = append(lines, fmt.Sprintf("- **Requires** %s: while it does not run or is `unavailable`, this plugin is `blocked` and its pages are hidden.", pluginLink(id, all)))
	}
	for _, id := range d.m.Uses {
		lines = append(lines, fmt.Sprintf("- **Uses** %s when it runs.", pluginLink(id, all)))
	}
	var req, uses []string
	for _, o := range all {
		if slices.Contains(o.m.Requires, d.m.ID) {
			req = append(req, pluginLink(o.m.ID, all))
		}
		if slices.Contains(o.m.Uses, d.m.ID) {
			uses = append(uses, pluginLink(o.m.ID, all))
		}
	}
	if len(req) > 0 {
		lines = append(lines, fmt.Sprintf("- **Required by** %s.", andList(req)))
	}
	if len(uses) > 0 {
		lines = append(lines, fmt.Sprintf("- **Used by** %s.", andList(uses)))
	}
	if len(lines) == 0 {
		return ""
	}
	return "A change to a plugin's settings restarts it and the plugins that require or use it.\n\n" + strings.Join(lines, "\n")
}

func pluginLink(id plugin.ID, all []docPlugin) string {
	for _, d := range all {
		if d.m.ID == id {
			return fmt.Sprintf("[%s](%s.md)", d.m.Name, id)
		}
	}
	return "`" + string(id) + "`"
}

func settingsDoc(id plugin.ID, vars []config.EnvVar) string {
	prefix := "plugins." + string(id) + "."
	var b strings.Builder
	fmt.Fprintf(&b, "Every setting is under `plugins.%s` in the config file and is also in the [configuration reference](../configuration.md#%s), with the rules for all of them: the layers, the `_FILE` form of each variable and secret references. _Restart_ marks a setting that applies only when dash restarts; a change to any other restarts the plugin in place.\n\n", id, githubAnchor("plugins."+string(id)))
	b.WriteString("| Setting | Variable | Type | Default | Secret | Restart | Description |\n|---|---|---|---|---|---|---|\n")
	for _, v := range vars {
		if !strings.HasPrefix(v.Path, prefix) {
			continue
		}
		def := "-"
		if v.Default != "" {
			def = "`" + v.Default + "`"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s | %s | %s | %s |\n", v.Path, v.Name, v.Type, def, yesNoCell(v.Secret), yesNoCell(v.Restart), strings.ReplaceAll(v.Description, "|", `\|`))
	}
	return strings.TrimSpace(b.String())
}

func yesNoCell(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// githubAnchor is the fragment GitHub gives a heading: lower case, spaces to hyphens, and every
// character but letters, digits, hyphens and underscores dropped.
func githubAnchor(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

var confirmText = map[model.Confirm]string{
	plugin.ConfirmNone:     "none",
	plugin.ConfirmDialog:   "the web UI asks in a dialog",
	plugin.ConfirmTypeName: "the caller types the target's name",
}

func actionsDoc(m plugin.Manifest, vars []config.EnvVar) (string, error) {
	if len(m.Actions) == 0 {
		return "None: the plugin only reads.", nil
	}
	env := func(p string) string {
		for _, v := range vars {
			if v.Path == p {
				return v.Name
			}
		}
		return ""
	}
	var b strings.Builder
	admin := true
	for _, a := range m.Actions {
		role := cmpRole(a.MinRole)
		admin = admin && role == auth.RoleAdmin
		fmt.Fprintf(&b, "### `%s`: %s\n\n`POST /api/v1/plugins/%s/actions/%s`, for %s. ", a.Name, a.Title, m.ID, a.Name, roleText(role))
		if a.Destructive {
			b.WriteString("Destructive. ")
		}
		if a.Idempotent {
			b.WriteString("Idempotent: repeating it with the same input is safe. ")
		} else {
			b.WriteString("Not idempotent. ")
		}
		fmt.Fprintf(&b, "Confirmation: %s.\n\n", confirmText[a.Confirm])
		in, err := inputDoc(a)
		if err != nil {
			return "", fmt.Errorf("action %s: %w", a.Name, err)
		}
		if in != "" {
			fmt.Fprintf(&b, "Input, a JSON object: %s.\n\n", in)
		}
	}
	n, window := api.ActionRateLimit()
	b.WriteString("### Limits\n\n")
	enabled := "plugins." + string(m.ID) + ".actions.enabled"
	fmt.Fprintf(&b, "- **Off by default.** The actions are mounted only while `%s` is `true` (`%s`); until then their routes answer 404 and the web UI shows no button for them. A change restarts the plugin.\n", enabled, env(enabled))
	if admin {
		b.WriteString("- **Administrators only.** A viewer gets 403 `forbidden`.\n")
	}
	fmt.Fprintf(&b, "- **Rate limit.** One user may start %d actions %s, across every plugin's actions; more answer 429 `rate_limited` with `Retry-After`.\n", n, perWindow(window))
	b.WriteString("- **Strict input.** The body is JSON with only the fields above; a missing required field or a value outside its list answers 400 `invalid_input`.\n")
	if deny := "plugins." + string(m.ID) + ".actions.denyNamespaces"; env(deny) != "" {
		fmt.Fprintf(&b, "- **Denied namespaces.** `%s` (`%s`) lists namespaces where the actions answer 403, whatever the role.\n", deny, env(deny))
	}
	b.WriteString("- **Audited.** Each run is written to the audit log before it runs and again with its outcome; when that record cannot be written the action is refused (503 `audit_unavailable`). The 403 and 429 refusals above are recorded as denials, at most one per user (or, before sign-in, per peer) and reason a minute; a 404 or a 400 `invalid_input` is not.\n")
	if slices.ContainsFunc(pluginRBAC[m.ID], func(g rbacGroup) bool { return g.guardrail }) {
		b.WriteString("- **Guardrail.** On Kubernetes, the Helm chart's admission policy holds dash's ServiceAccount to exactly the write these actions make, whatever RBAC grants: see [the guardrail](../deploy/kubernetes.md#the-guardrail).\n")
	}
	return strings.TrimSpace(b.String()), nil
}

// cmpRole is an action's lowest role: admin unless it says otherwise.
func cmpRole(r auth.Role) auth.Role {
	if r == "" {
		return auth.RoleAdmin
	}
	return r
}

func roleText(r auth.Role) string {
	if r == auth.RoleAdmin {
		return "administrators"
	}
	return "viewers and administrators"
}

func perWindow(w time.Duration) string {
	if w == time.Minute {
		return "a minute"
	}
	return "per " + w.String()
}

// inputDoc lists the fields of an action's input in declaration order, with their JSON type,
// whether they are required and the values an enum allows.
func inputDoc(a plugin.ActionSpec) (string, error) {
	if a.Input == nil {
		return "", nil
	}
	v := a.Input()
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	s, err := jsonschema.Document(reflect.New(t).Elem().Interface())
	if err != nil {
		return "", err
	}
	props, _ := s["properties"].(jsonschema.Schema)
	required, _ := s["required"].([]string)
	var fields []string
	for _, name := range jsonFields(t) {
		p, _ := props[name].(jsonschema.Schema)
		desc := fmt.Sprint(p["type"])
		if slices.Contains(required, name) {
			desc += ", required"
		}
		if enum, ok := p["enum"].([]any); ok {
			vals := make([]string, len(enum))
			for i, e := range enum {
				vals[i] = fmt.Sprintf("`%v`", e)
			}
			desc += ", one of " + strings.Join(vals, ", ")
		}
		fields = append(fields, fmt.Sprintf("`%s` (%s)", name, desc))
	}
	return strings.Join(fields, "; "), nil
}

// jsonFields are the JSON names of t's fields in declaration order, embedded structs flattened.
func jsonFields(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct:
			out = append(out, jsonFields(f.Type)...)
		case name == "-" || !f.IsExported():
		case name == "":
			out = append(out, f.Name)
		default:
			out = append(out, name)
		}
	}
	return out
}

// rbacGroup is a set of Kubernetes rules a plugin needs under one condition. chart is the Helm
// value that makes charts/dash grant it, "" when the chart grants none; a test in charts holds
// the generated pages to what the chart renders.
type rbacGroup struct {
	when       string
	chart      string
	namespaced bool // a Role in each namespace instead of the ClusterRole
	guardrail  bool // an action's write, which the chart's guardrail policy fences
	rules      []rbacRule
}

type rbacRule struct {
	groups, resources, names, verbs []string
	comment                         string
}

var (
	readVerbs  = []string{"get", "list", "watch"}
	fluxReads  = []rbacRule{{groups: []string{"kustomize.toolkit.fluxcd.io"}, resources: []string{"kustomizations"}}, {groups: []string{"helm.toolkit.fluxcd.io"}, resources: []string{"helmreleases"}}, {groups: []string{"source.toolkit.fluxcd.io"}, resources: []string{"gitrepositories", "ocirepositories", "helmrepositories"}}}
	pluginRBAC = map[plugin.ID][]rbacGroup{
		"kubernetes": {
			{when: "To read the cluster", chart: "rbac.read", rules: []rbacRule{
				{groups: []string{""}, resources: []string{"nodes", "namespaces", "pods", "persistentvolumeclaims", "events"}, verbs: readVerbs},
				{groups: []string{"apps"}, resources: []string{"deployments", "statefulsets", "daemonsets", "replicasets"}, verbs: readVerbs},
				{groups: []string{"batch"}, resources: []string{"jobs", "cronjobs"}, verbs: readVerbs},
				{groups: []string{"cert-manager.io"}, resources: []string{"certificates"}, verbs: readVerbs, comment: "The CRDs the plugin reads when the cluster has them."},
				{groups: []string{"external-secrets.io"}, resources: []string{"externalsecrets"}, verbs: readVerbs},
				{groups: []string{"gateway.networking.k8s.io"}, resources: []string{"httproutes"}, verbs: readVerbs},
			}},
			{when: "With `plugins.kubernetes.apps.ingress: true`", chart: "config.plugins.kubernetes.apps.ingress", rules: []rbacRule{
				{groups: []string{"networking.k8s.io"}, resources: []string{"ingresses"}, verbs: readVerbs},
			}},
			{when: "With `plugins.kubernetes.actions.enabled` (the restart action)", chart: "rbac.actions.restart", guardrail: true, rules: []rbacRule{
				{groups: []string{""}, resources: []string{"namespaces"}, verbs: []string{"get"}, comment: "The protected-label check reads the namespace first."},
				{groups: []string{"apps"}, resources: []string{"deployments", "statefulsets", "daemonsets"}, verbs: []string{"patch"}, comment: "The pod template's restartedAt annotation."},
			}},
			{when: "For `k8s:` secret references", chart: "rbac.secretRefs", namespaced: true, rules: []rbacRule{
				{groups: []string{""}, resources: []string{"secrets"}, names: []string{"<name>"}, verbs: readVerbs, comment: "One name per Secret a k8s: reference names in this namespace."},
			}},
			{when: "With `plugins.kubernetes.upstreamViaAPIServer` (development)", rules: []rbacRule{
				{groups: []string{""}, resources: []string{"services/proxy"}, verbs: []string{"get", "create"}, comment: "A proxied GET is authorized as get, a proxied POST as create."},
			}},
		},
		"flux": {
			{when: "To read the Flux objects", chart: "rbac.read", rules: withVerbs(fluxReads, readVerbs)},
			{when: "With `plugins.flux.actions.enabled` (the reconcile action)", chart: "rbac.actions.fluxReconcile", guardrail: true, rules: withVerbs(fluxReads, []string{"patch"})},
		},
		"cnpg": {
			{when: "To read the CloudNativePG clusters", chart: "rbac.read", rules: []rbacRule{
				{groups: []string{"postgresql.cnpg.io"}, resources: []string{"clusters"}, verbs: readVerbs},
			}},
		},
	}
)

// withVerbs is rules, each with verbs.
func withVerbs(rules []rbacRule, verbs []string) []rbacRule {
	out := slices.Clone(rules)
	for i := range out {
		out[i].verbs = verbs
	}
	return out
}

func rbacDoc(id plugin.ID) string {
	groups := pluginRBAC[id]
	if len(groups) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("dash reads the cluster with its ServiceAccount. The Helm chart grants each group of rules below while the value named is set ([RBAC modes](../deploy/kubernetes.md#rbac-modes)); with `rbac.create: false`, grant them yourself.\n")
	for _, g := range groups {
		where := "(chart: `" + g.chart + "`)"
		if g.chart == "" {
			where = "(the chart grants none)"
		}
		kind := "the ClusterRole"
		if g.namespaced {
			kind = "a Role in each namespace"
		}
		fmt.Fprintf(&b, "\n**%s** %s, in %s:\n\n```yaml\nrules:\n", g.when, where, kind)
		for _, r := range g.rules {
			if r.comment != "" {
				fmt.Fprintf(&b, "  # %s\n", r.comment)
			}
			fmt.Fprintf(&b, "  - apiGroups: [%s]\n    resources: [%s]\n", yamlList(r.groups), yamlList(r.resources))
			if len(r.names) > 0 {
				fmt.Fprintf(&b, "    resourceNames: [%s]\n", yamlList(r.names))
			}
			fmt.Fprintf(&b, "    verbs: [%s]\n", yamlList(r.verbs))
		}
		b.WriteString("```\n")
	}
	return strings.TrimSpace(b.String())
}

// yamlList is a flow sequence; the empty string (the core API group) and names with angle
// brackets are quoted.
func yamlList(items []string) string {
	out := make([]string, len(items))
	for i, s := range items {
		if s == "" || strings.ContainsAny(s, "<>") {
			s = `"` + s + `"`
		}
		out[i] = s
	}
	return strings.Join(out, ", ")
}

func demoDoc(d docPlugin) string {
	if _, ok := d.p.(plugin.Demo); !ok {
		return "None: in demo mode (`DASH_DEMO=true`) the plugin does not run."
	}
	s := "With `DASH_DEMO=true` dash runs the plugin's demo twin instead: the same manifest, enabled and configured whatever the settings say, publishing made-up data from the fake lab in `internal/demo/world` and contacting nothing."
	if len(d.m.Actions) > 0 {
		s += " Its actions are mounted and change nothing outside dash."
	}
	return s
}

// andList joins items as "a", "a and b" or "a, b and c".
func andList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// pluginIndex is docs/plugins/README.md.
func pluginIndex(all []docPlugin) string {
	var b strings.Builder
	b.WriteString("<!-- Generated by `dash docs plugins`; do not edit. Run `make generate`. -->\n\n# Plugins\n\n")
	b.WriteString("Everything dash shows comes from a plugin. A plugin turns itself on when its settings are complete (rustfs is off unless `enabled: true`), and `plugins.<id>.enabled: false` turns it off; its pages are hidden while it does not run. Each page below is generated from the plugin's manifest, its settings schema and its hand-written notes (`cmd/dash/plugindocs/<id>.md`).\n\n")
	b.WriteString("| Plugin | What | Pages | Actions |\n|---|---|---|---|\n")
	for _, d := range all {
		var pages, actions []string
		for _, n := range d.m.UI.Nav {
			pages = append(pages, n.Title)
		}
		for _, a := range d.m.Actions {
			actions = append(actions, "`"+a.Name+"`")
		}
		fmt.Fprintf(&b, "| [%s](%s.md) (`%s`) | %s | %s | %s |\n", d.m.Name, d.m.ID, d.m.ID, strings.ReplaceAll(d.m.Description, "|", `\|`), orDash(strings.Join(pages, ", ")), orDash(strings.Join(actions, ", ")))
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
