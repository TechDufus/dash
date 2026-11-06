package mcp

import (
	"context"
	"embed"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechDufus/dash/internal/bundle"
	"github.com/TechDufus/dash/internal/model"
)

//go:embed prompts/*.md
var promptFiles embed.FS

// digestSinces are the periods dash_daily_digest takes.
var digestSinces = []string{"6h", "24h", "7d"}

// promptArgs are what a prompt's text reads, validated before it is rendered.
type promptArgs struct{ Ref, Since string }

// render fills in text's placeholders: `{{.Ref}}` (with its backticks) becomes the ref in a code
// span it cannot close, {{.Since}} the period. A plain replacer, not text/template: that
// package's reflect.MethodByName turns off the linker's dead-code elimination (about 27 MB of
// binary; internal/hygiene keeps both template packages out).
func (a promptArgs) render(text string) string {
	return strings.NewReplacer("`{{.Ref}}`", bundle.CodeSpan(a.Ref), "{{.Since}}", a.Since).Replace(text)
}

type prompt struct {
	name, title, desc string
	args              []*sdk.PromptArgument
	// check validates the arguments and fills in defaults.
	check func(map[string]string) (promptArgs, error)
}

var refArg = &sdk.PromptArgument{Name: "ref", Title: "Object ref", Required: true,
	Description: "The object's ref, <plugin>:<Kind>/<id>, as dash_search or dash_overview returns it."}

func noArgs(map[string]string) (promptArgs, error) { return promptArgs{}, nil }

// checkRef accepts a well-formed ref, of kind when kind is not empty; a refusal shows a ref.
func checkRef(kind string) func(map[string]string) (promptArgs, error) {
	example := refExample
	if kind == "Alert" {
		example = "alertmanager:Alert/<fingerprint>"
	}
	return func(a map[string]string) (promptArgs, error) {
		ref := a["ref"]
		_, k, _, ok := model.ParseRef(ref)
		var msg string
		switch {
		case ref == "":
			msg = "ref is required, e.g. " + example
		case !ok:
			msg = "ref must be <plugin>:<Kind>/<id>, e.g. " + example
		case kind != "" && k != kind:
			msg = "ref must be the ref of an " + kind + ", e.g. " + example + "; find one with dash_search"
		default:
			return promptArgs{Ref: ref}, nil
		}
		return promptArgs{}, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: msg}
	}
}

// prompts are the four prompts of design N.3, listed in this order.
var prompts = []prompt{
	{name: "dash_triage", title: "Triage the homelab", check: noArgs,
		desc: "Go through what needs attention now, most urgent first, with evidence and next steps."},
	{name: "dash_investigate", title: "Investigate an object", args: []*sdk.PromptArgument{refArg}, check: checkRef(""),
		desc: "Find out what is happening to one object (a pod, node, VM, alert ...) and why."},
	{name: "dash_daily_digest", title: "Daily digest", check: func(a map[string]string) (promptArgs, error) {
		since := a["since"]
		if since == "" {
			since = "24h"
		}
		if !slices.Contains(digestSinces, since) {
			return promptArgs{}, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "since must be one of " + strings.Join(digestSinces, ", ")}
		}
		return promptArgs{Since: since}, nil
	}, args: []*sdk.PromptArgument{{Name: "since", Title: "Period", Description: "6h, 24h (the default) or 7d."}},
		desc: "Sum up the homelab's health, what changed and the trends over a period."},
	{name: "dash_explain_alert", title: "Explain an alert", args: []*sdk.PromptArgument{refArg}, check: checkRef("Alert"),
		desc: "Explain what an Alertmanager alert means and whether the evidence bears it out."},
}

// addPrompts registers the prompts, each rendered from internal/mcp/prompts/<name>.md.
func (s *Server) addPrompts() {
	for _, p := range prompts {
		raw, err := promptFiles.ReadFile("prompts/" + p.name + ".md")
		if err != nil {
			panic(err) // the files are embedded: only a renamed file gets here, and a test catches it
		}
		text := string(raw)
		s.srv.AddPrompt(&sdk.Prompt{Name: p.name, Title: p.title, Description: p.desc, Arguments: p.args},
			func(_ context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
				args, err := p.check(req.Params.Arguments)
				if err != nil {
					return nil, err
				}
				return &sdk.GetPromptResult{Description: p.desc, Messages: []*sdk.PromptMessage{
					{Role: "user", Content: &sdk.TextContent{Text: args.render(text)}},
				}}, nil
			})
	}
}
