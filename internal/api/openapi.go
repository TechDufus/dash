package api

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/jsonschema"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// OpenAPI returns the OpenAPI 3.1 document of the /api/v1 contract (design L.3), generated from
// the route table and the types it names. It is a function of the code alone: the same
// manifests give the same bytes, so the file in docs/api can be diffed in CI.
//
// manifests are the plugins to document: each one's actions (as if enabled) and, through
// routes, its GET routes. routes may be nil. The unversioned legacy routes are not in the
// document: the contract is /api/v1, and they disappear as their plugins migrate.
func OpenAPI(manifests []plugin.Manifest, routes func(plugin.ID) []plugin.RouteInfo) (doc []byte, err error) {
	defer func() { // the reflector panics on a type it cannot describe
		if p := recover(); p != nil {
			doc, err = nil, fmt.Errorf("openapi: %v", p)
		}
	}()
	b := &specBuilder{
		refl:      jsonschema.New(jsonschema.WithRefPrefix("#/components/schemas/")),
		manifests: manifests, routes: routes,
		paths: map[string]map[string]any{}, tags: map[string]string{},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(b.build()); err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	return buf.Bytes(), nil
}

type object = map[string]any

type specBuilder struct {
	refl      *jsonschema.Reflector
	manifests []plugin.Manifest
	routes    func(plugin.ID) []plugin.RouteInfo
	paths     map[string]map[string]any
	tags      map[string]string // name -> description
}

var tagDocs = map[string]string{
	"Session": "Who is calling.",
	"Topics":  "Live state: one envelope per topic, as a snapshot or a stream.",
	"Series":  "Named metric series.",
	"Plugins": "The plugin instances and the generic plugin routes.",
}

var pathVarRE = regexp.MustCompile(`\{([A-Za-z0-9_]+)(?:\.\.\.)?\}`)

func (b *specBuilder) build() object {
	for _, r := range (&Server{}).routeTable() {
		if r.Legacy || r.Plugin || r.Infra {
			continue
		}
		b.add(r.Path, r.Method, b.operation(r), r.Tag, tagDocs[r.Tag])
	}
	b.addPlugins()
	envelopes := b.topicEnvelopes()
	b.refl.Type(reflect.TypeFor[model.APIError]())
	schemas := object{}
	for name, s := range b.refl.Defs() {
		schemas[name] = s
	}
	// The codes are the contract: a client may switch on them.
	code := schemas["APIError"].(jsonschema.Schema)["properties"].(jsonschema.Schema)["code"].(jsonschema.Schema)
	code["enum"] = model.ErrCodes()
	code["description"] = "Machine-readable reason; branch on this, not on the error text."
	for name, s := range envelopes {
		schemas[name] = s
	}

	tags := make([]object, 0, len(b.tags))
	for _, name := range slices.Sorted(maps.Keys(b.tags)) {
		t := object{"name": name}
		if d := b.tags[name]; d != "" {
			t["description"] = d
		}
		tags = append(tags, t)
	}
	return object{
		"openapi": "3.1.0",
		"info": object{
			"title":   "dash API",
			"version": "v1",
			"description": "The REST and server-sent-events API of dash, the plugin-based infrastructure dashboard. " +
				"Every response carries an X-Request-Id header, and every error is the APIError body.",
			"contact": object{"name": "dash", "url": "https://github.com/TechDufus/dash"},
			"license": object{"name": "MIT", "identifier": "MIT"},
		},
		"servers":  []object{{"url": "/", "description": "The dash instance that served this document."}},
		"tags":     tags,
		"paths":    b.paths,
		"security": []object{{"identityProxy": []string{}}},
		"components": object{
			"schemas": schemas,
			"securitySchemes": object{
				"identityProxy": object{
					"type": "apiKey", "in": "header", "name": "X-Forwarded-Preferred-Username",
					"description": "The user the identity proxy in front of dash authenticated; dash trusts it as set by its " +
						"network peer, so it is not something a client sends itself. Dev and demo modes supply a fixed user.",
				},
			},
			"headers": object{
				"XRequestId": object{
					"description": "Identifies the request in logs and audit records; repeated as requestId in an error body.",
					"schema":      object{"type": "string"},
				},
			},
			"responses": object{
				"Error": object{
					"description": "The request failed; code says why.",
					"headers":     object{"X-Request-Id": object{"$ref": "#/components/headers/XRequestId"}},
					"content":     jsonContent(object{"$ref": "#/components/schemas/APIError"}),
				},
			},
		},
	}
}

// add registers an operation under path and method, and its tag.
func (b *specBuilder) add(path, method string, op object, tag, tagDoc string) {
	if b.paths[path] == nil {
		b.paths[path] = map[string]any{}
	}
	b.paths[path][strings.ToLower(method)] = op
	if _, ok := b.tags[tag]; !ok || tagDoc != "" {
		b.tags[tag] = tagDoc
	}
}

func jsonContent(schema any) object {
	return object{"application/json": object{"schema": schema}}
}

func okHeaders() object {
	return object{"X-Request-Id": object{"$ref": "#/components/headers/XRequestId"}}
}

// operation describes one route of the table.
func (b *specBuilder) operation(r Route) object {
	op := object{
		"operationId": r.ID, "summary": r.Summary, "tags": []string{r.Tag},
		"x-dash-min-role": string(r.Role),
	}
	if r.Scope != "" {
		op["x-dash-scope"] = r.Scope
	}
	op["parameters"] = params(r.Path, r.PathParams, r.Query)
	if r.Request != nil {
		op["requestBody"] = object{"required": true, "content": jsonContent(b.typeSchema(r.Request))}
	}

	status := fmt.Sprint(cmp.Or(r.Status, http.StatusOK))
	responses := object{"default": object{"$ref": "#/components/responses/Error"}}
	ok := object{"description": cmp.Or(r.Summary, "Success"), "headers": okHeaders()}
	switch {
	case r.SSE:
		ok["description"] = cmp.Or(r.SSEDoc, "A stream of server-sent events. Each event is named after its topic and carries "+
			"that topic's envelope as JSON; a comment line every 15 seconds keeps the connection alive.")
		ok["content"] = object{"text/event-stream": object{"schema": object{"type": "string"}}}
	case r.TopicEnvelope:
		ok["description"] = "The topic's current envelope."
		ok["headers"] = object{
			"X-Request-Id": object{"$ref": "#/components/headers/XRequestId"},
			"ETag":         object{"description": "Identifies this content; send it back as If-None-Match.", "schema": object{"type": "string"}},
		}
		ok["content"] = jsonContent(object{"oneOf": b.envelopeRefs()})
		responses["304"] = object{"description": "The envelope still has the ETag in If-None-Match.", "headers": okHeaders()}
	case r.Response != nil:
		ok["content"] = jsonContent(b.typeSchema(r.Response))
	}
	responses[status] = ok
	op["responses"] = responses
	return op
}

// typeSchema is the schema of the type of v: a $ref to its component for a named struct.
func (b *specBuilder) typeSchema(v any) jsonschema.Schema {
	return b.refl.Type(reflect.TypeOf(v))
}

// params lists the path and query parameters; a path parameter is every {name} of the path. A path
// parameter nobody documented (a plugin route's) is described by its place in the path.
func params(path string, pathDocs, query []plugin.Param) []object {
	out := []object{}
	doc := map[string]string{}
	for _, p := range pathDocs {
		doc[p.Name] = p.Doc
	}
	for _, m := range pathVarRE.FindAllStringSubmatch(path, -1) {
		p := object{"name": m[1], "in": "path", "required": true, "schema": object{"type": "string"}}
		p["description"] = cmp.Or(doc[m[1]], "The {"+m[1]+"} segment of the request path.")
		out = append(out, p)
	}
	for _, q := range query {
		p := object{"name": q.Name, "in": "query", "schema": object{"type": "string"}}
		if q.Required {
			p["required"] = true
		}
		if q.Doc != "" {
			p["description"] = q.Doc
		}
		out = append(out, p)
	}
	return out
}

// addPlugins documents each registered plugin: its actions and its read routes.
func (b *specBuilder) addPlugins() {
	for _, m := range b.manifests {
		id := string(m.ID)
		tagDoc := m.Name
		if m.Description != "" {
			tagDoc += ": " + m.Description
		}
		base := "/api/v1/plugins/" + id
		for _, a := range m.Actions {
			op := object{
				"operationId":        id + "_" + strings.ReplaceAll(a.Name, ".", "_"),
				"summary":            cmp.Or(a.Title, a.Name),
				"tags":               []string{id},
				"parameters":         []object{},
				"x-dash-min-role":    string(cmp.Or(a.MinRole, "admin")),
				"x-dash-scope":       "actions:" + id,
				"x-dash-confirm":     string(cmp.Or(a.Confirm, "none")),
				"x-dash-destructive": a.Destructive,
				"x-dash-idempotent":  a.Idempotent,
				"description": "Runs only while the instance is running with actions.enabled set; otherwise 404. " +
					"A call that passes the role, rate-limit, input and namespace checks is recorded in the audit log " +
					"before it runs and again with its outcome. A refusal for role, rate limit or denied namespace is " +
					"recorded as a denial (at most one per peer and reason a minute); a request with an invalid body (400) " +
					"or for an unknown or unavailable action (404) is not recorded. Every call by a principal with the " +
					"required role counts against the principal's action rate limit.",
			}
			if t := inputType(a); t != nil {
				op["requestBody"] = object{"required": true, "description": "The action's input.", "content": jsonContent(b.refl.Type(t))}
			}
			op["responses"] = object{
				"200":     object{"description": "The action's result.", "headers": okHeaders(), "content": jsonContent(object{})},
				"202":     object{"description": "The action was accepted and has no result.", "headers": okHeaders(), "content": jsonContent(object{"type": "object", "properties": object{"status": object{"const": "accepted"}}})},
				"default": object{"$ref": "#/components/responses/Error"},
			}
			b.add(base+"/actions/"+a.Name, "POST", op, id, tagDoc)
		}
		if b.routes == nil {
			continue
		}
		for _, ri := range b.routes(m.ID) {
			path := base + strings.ReplaceAll(ri.Pattern, "{$}", "")
			r := Route{
				ID:     id + "_get" + strings.NewReplacer("/", "_", "{", "", "}", "", "...", "", ".", "_").Replace(strings.TrimSuffix(path, "/")[len(base):]),
				Method: "GET", Path: path, Summary: ri.Spec.Summary, Tag: id, Role: "viewer", Scope: ri.Spec.Scope,
				Query: ri.Spec.Query, Response: ri.Spec.Response,
				SSE: ri.Stream != nil, SSEDoc: pluginStreamDoc,
			}
			b.add(pathVarRE.ReplaceAllString(path, "{$1}"), "GET", b.operation(r), id, tagDoc)
		}
	}
}

// topicTypes is the payload type of every topic: the core topics and the plugins' own.
func (b *specBuilder) topicTypes() map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for t, typ := range model.TopicTypes() {
		out[string(t)] = typ
	}
	for _, m := range b.manifests {
		for _, ts := range m.Topics {
			name := string(m.ID) + "." + ts.Name
			if ts.Legacy != "" {
				name = ts.Legacy
			}
			var typ reflect.Type
			if ts.Type != nil {
				typ = reflect.TypeOf(ts.Type)
				for typ.Kind() == reflect.Pointer {
					typ = typ.Elem()
				}
			}
			if prev, dup := out[name]; dup && typ != nil && prev != typ {
				panic(fmt.Sprintf("topic %s is declared with two payload types, %s and %s", name, prev, typ))
			}
			if _, dup := out[name]; !dup || typ != nil {
				out[name] = typ
			}
		}
	}
	return out
}

const envelopePrefix = "Envelope."

// topicEnvelopes is the schema of the envelope of each topic: the common fields and its own
// typed data. The component name is Envelope.<topic>.
func (b *specBuilder) topicEnvelopes() map[string]any {
	source := b.refl.Type(reflect.TypeFor[model.SourceStatus]())
	out := map[string]any{}
	for name, typ := range b.topicTypes() {
		data := jsonschema.Schema{}
		if typ != nil {
			data = b.refl.Type(typ)
		}
		out[envelopePrefix+name] = object{
			"type":                 "object",
			"description":          "The envelope of the " + name + " topic.",
			"required":             []string{"data", "sources", "topic", "updatedAt"},
			"additionalProperties": false,
			"properties": object{
				"topic":     object{"const": name},
				"updatedAt": object{"type": "string", "format": "date-time"},
				"sources":   object{"type": "array", "items": source},
				"data":      data,
			},
		}
	}
	return out
}

// envelopeRefs is a $ref to every topic envelope, in topic order.
func (b *specBuilder) envelopeRefs() []object {
	names := slices.Sorted(maps.Keys(b.topicTypes()))
	out := make([]object, len(names))
	for i, n := range names {
		out[i] = object{"$ref": "#/components/schemas/" + envelopePrefix + n}
	}
	return out
}
