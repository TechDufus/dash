package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
)

// errConfigInvalid makes `dash config validate` exit 1; the report it printed says why.
var errConfigInvalid = errors.New("configuration is invalid")

const configUsage = "dash config env [--markdown] | dash config schema | dash config validate [file] [--resolve-secrets]"

// configCommand runs `dash config <args>`.
func configCommand(args []string, out io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "env":
			return configEnv(args[1:], out)
		case "schema":
			if len(args) > 1 {
				return fmt.Errorf("unexpected argument %q; usage: dash config schema", args[1])
			}
			return config.WriteSchema(out)
		case "validate":
			return configValidate(args[1:], out)
		}
	}
	return fmt.Errorf("unknown command %q; usage: %s", strings.Join(append([]string{"config"}, args...), " "), configUsage)
}

// configEnv prints every setting with its environment variable: an aligned table, or with
// --markdown the page docs/configuration.md is generated from.
func configEnv(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("config env", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	markdown := fs.Bool("markdown", false, "write docs/configuration.md instead of the table")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w; usage: dash config env [--markdown]", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q; usage: dash config env [--markdown]", fs.Arg(0))
	}
	if *markdown {
		return config.WriteEnvMarkdown(out)
	}
	return config.WriteEnvTable(out)
}

// configValidate resolves the configuration exactly as startup does (the defaults, the file,
// the environment and the checks that follow, the secret keys among them) without opening the
// data directory, the settings store or any network connection, and prints every problem with
// its path and the layer that set the value. The file is the argument, or by default the one
// startup would read (DASH_CONFIG, else config.yaml in the data directory). The env: and file:
// references of secrets, the key files and the launcher file are not read unless asked: they
// often point at files that exist only where dash runs.
func configValidate(args []string, out io.Writer) error {
	const usage = "usage: dash config validate [file] [--resolve-secrets]"
	var file string
	resolveSecrets := false
	for _, a := range args {
		switch {
		case a == "--resolve-secrets":
			resolveSecrets = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q; %s", a, usage)
		case file == "":
			file = a
		default:
			return fmt.Errorf("unexpected argument %q; %s", a, usage)
		}
	}

	var src config.Sources
	var err error
	if file != "" {
		data, rerr := os.ReadFile(file)
		if rerr != nil {
			return fmt.Errorf("%w; %s", rerr, usage)
		}
		src = config.Sources{Environ: os.Environ(), File: &config.File{Path: file, Data: data}}
	} else if src, err = config.ProcessSources(nil); err != nil {
		return reportInvalid(out, err)
	}
	src.SkipSecrets = !resolveSecrets
	// A k8s: reference is read through the kubernetes plugin of a running dash: here its syntax is
	// checked and the handle stays pending.
	src.K8s = secret.NewK8sRefs(nil)
	var warnings bytes.Buffer
	src.Log = slog.New(slog.NewTextHandler(&warnings, &slog.HandlerOptions{
		Level: slog.LevelWarn,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey || a.Key == slog.LevelKey {
				return slog.Attr{}
			}
			return a
		},
	}))

	// The checks startup adds around Resolve (loadConfig): the secret keys. A key file and the
	// launcher file are files that may exist only where dash runs, so they are read only with
	// --resolve-secrets (the launcher plugin reads its file at Start; this reads it now); the
	// rest of the key rules apply either way.
	var keyErr error
	if resolveSecrets {
		_, keyErr = config.SecretKeys(src.Environ, nil)
	} else {
		keyErr = config.CheckSecretKeys(src.Environ)
	}
	res, resErr := config.Resolve(src)
	if resErr == nil && resolveSecrets {
		if f := res.Tree.Plugins.Launcher.File; f != "" {
			_, resErr = pluginconf.LoadLauncher(f)
		}
	}
	if err := errors.Join(keyErr, resErr); err != nil {
		printWarnings(out, &warnings)
		return reportInvalid(out, err)
	}
	printWarnings(out, &warnings)
	from := "no config file"
	if src.File != nil {
		from = "config file " + src.File.Path
	}
	fmt.Fprintf(out, "configuration valid (%s, and the environment)\n", from)
	if src.SkipSecrets {
		fmt.Fprintln(out, "env: and file: secret references were not read; --resolve-secrets reads them")
	}
	return nil
}

func printWarnings(out io.Writer, warnings *bytes.Buffer) {
	for line := range strings.SplitSeq(strings.TrimSpace(warnings.String()), "\n") {
		if line != "" {
			fmt.Fprintln(out, "warning:", line)
		}
	}
}

// reportInvalid prints every problem in err, one per entry, and returns errConfigInvalid.
func reportInvalid(out io.Writer, err error) error {
	problems := flatten(err)
	fmt.Fprintf(out, "configuration invalid: %d problem", len(problems))
	if len(problems) != 1 {
		fmt.Fprint(out, "s")
	}
	fmt.Fprintln(out)
	for _, p := range problems {
		fmt.Fprintf(out, "  %s\n", strings.ReplaceAll(strings.TrimSpace(p), "\n", "\n  "))
	}
	return errConfigInvalid
}

// flatten lists the errors joined in err, depth first.
func flatten(err error) []string {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var out []string
		for _, e := range j.Unwrap() {
			out = append(out, flatten(e)...)
		}
		return out
	}
	return []string{err.Error()}
}
