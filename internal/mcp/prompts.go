package mcp

import (
	"context"
	"embed"
	"slices"
	"strings"
	"text/template"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechDufus/dash/internal/model"
)

//go:embed prompts/*.md
var promptFiles embed.FS

// digestSinces are the periods dash_daily_digest takes.
var digestSinces = []string{"6h", "24h", "7d"}

// promptArgs are what a prompt's template reads, validated before it is rendered.
type promptArgs struct{ Ref, Since string }

type prompt struct {
	name, title, desc string
	args              []*sdk.PromptArgument
	// check validates the arguments and fills in defaults.
	check func(map[string]string) (promptArgs, error)
}

var refArg = &sdk.PromptArgument{Name: "ref", Title: "Object ref", Required: true,
	Description: "The object's ref, <plugin>:<Kind>/<id>, as dash_search or dash_overview returns it."}

func noArgs(map[string]string) (promptArgs, error) { return promptArgs{}, nil }

// checkRef accepts a well-formed ref, of kind when kind is not empty.
func checkRef(kind string) func(map[string]string) (promptArgs, error) {
	return func(a map[string]string) (promptArgs, error) {
		_, k, _, ok := model.ParseRef(a["ref"])
		if !ok || (kind != "" && k != kind) {
			want := "a ref"
			if kind != "" {
				want = "the ref of an " + kind
			}
			return promptArgs{}, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "ref must be " + want}
		}
		return promptArgs{Ref: a["ref"]}, nil
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
		text, err := promptFiles.ReadFile("prompts/" + p.name + ".md")
		if err != nil {
			panic(err) // the files are embedded: only a renamed file gets here, and a test catches it
		}
		tmpl := template.Must(template.New(p.name).Option("missingkey=error").Parse(string(text)))
		s.srv.AddPrompt(&sdk.Prompt{Name: p.name, Title: p.title, Description: p.desc, Arguments: p.args},
			func(_ context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
				args, err := p.check(req.Params.Arguments)
				if err != nil {
					return nil, err
				}
				var b strings.Builder
				if err := tmpl.Execute(&b, args); err != nil {
					return nil, err
				}
				return &sdk.GetPromptResult{Description: p.desc, Messages: []*sdk.PromptMessage{
					{Role: "user", Content: &sdk.TextContent{Text: b.String()}},
				}}, nil
			})
	}
}
