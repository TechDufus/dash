package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/config"
)

// errAuditFailed makes `dash audit verify` exit 1; the report it printed says why.
var errAuditFailed = errors.New("audit log failed verification")

// auditVerify checks the hash chain of the audit log in --dir, by default the data directory
// the server would use (file and environment layers, as for the settings store), and lists
// intents that never got an outcome (a crash mid-action).
func auditVerify(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("audit verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", "", "directory holding audit.jsonl (default: the configured data directory)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w; usage: dash audit verify [--dir DIR]", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q; usage: dash audit verify [--dir DIR]", fs.Arg(0))
	}
	if *dir == "" {
		src, err := config.ProcessSources(nil) // verification reads no secret
		if err != nil {
			return err
		}
		boot, err := config.BootstrapOf(src)
		if err != nil {
			return err
		}
		*dir = boot.DataDir
	}

	rep, err := audit.Verify(*dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %d records in %d files\n", *dir, rep.Records, len(rep.Files))
	if rep.Break != nil {
		fmt.Fprintf(out, "BROKEN: %s\n", rep.Break)
	} else {
		fmt.Fprintln(out, "chain intact")
	}
	for _, d := range rep.Dangling {
		target := strings.Trim(d.Target.Namespace+"/"+d.Target.Name, "/")
		fmt.Fprintf(out, "dangling intent %s: %s %s %s at %s (%s:%d)\n",
			d.ID, d.Action, d.Target.Kind, target, d.Time.Format(time.RFC3339), d.File, d.Line)
	}
	if !rep.OK() || len(rep.Dangling) > 0 {
		return errAuditFailed
	}
	return nil
}
