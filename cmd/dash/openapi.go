package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/plugin"
)

const apiUsage = "dash api openapi [-o FILE]"

// apiCommand runs `dash api <args>`.
func apiCommand(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "openapi" {
		return fmt.Errorf("unknown command %q; usage: %s", strings.Join(append([]string{"api"}, args...), " "), apiUsage)
	}
	return openapiCommand(args[1:], out)
}

// openapiCommand prints the OpenAPI 3.1 document of the API: what `make generate` writes to
// docs/api/openapi.json and `make check` compares. It describes the plugins dash ships, with
// every action enabled, and needs no configuration, data directory or network.
func openapiCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("api openapi", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	file := fs.String("o", "", "write the document to this file instead of standard output")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w; usage: %s", err, apiUsage)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q; usage: %s", fs.Arg(0), apiUsage)
	}
	doc, err := api.OpenAPI(builtinManifests(), builtinRoutes)
	if err != nil {
		return err
	}
	if *file != "" {
		return os.WriteFile(*file, doc, 0o644)
	}
	_, err = out.Write(doc)
	return err
}

// builtinRoutes are the GET routes and streams of a plugin dash ships, read off a fresh instance
// that is never started.
func builtinRoutes(id plugin.ID) []plugin.RouteInfo {
	if p, ok := builtinPlugin(id); ok {
		if r, ok := p.(plugin.Router); ok {
			return plugin.CollectRoutes(r)
		}
	}
	return nil
}
