// Command release generates, signs and verifies the signed update feed
// (updates.json) and builds the local demo feed. See docs/updates-feed.md
// and demo/README.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const usage = `usage: release <command> [flags]

commands:
  keygen    generate an Ed25519 signing key
  generate  build updates.json from versions.yml and an artifacts file
  sign      write updates.json.sig
  verify    verify updates.json against a public key
  demo      generate the local demo feed (demo/feed/rev-1 .. rev-7)

run "release <command> -h" for flags.
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "release:", err)
		}
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return flag.ErrHelp
	}
	cmds := map[string]func([]string, io.Writer) error{
		"keygen":   cmdKeygen,
		"generate": cmdGenerate,
		"sign":     cmdSign,
		"verify":   cmdVerify,
		"demo":     cmdDemo,
	}
	cmd, ok := cmds[args[0]]
	if !ok {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
	return cmd(args[1:], stdout)
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("release "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// parse parses args and rejects positional arguments and missing required flags.
func parse(fs *flag.FlagSet, args []string, required ...string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	var missing []string
	for _, r := range required {
		if !set[r] {
			missing = append(missing, "--"+r)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required flag(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

// listFlag collects a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(s string) error { *l = append(*l, s); return nil }

// timeFlag is an RFC 3339 timestamp flag.
type timeFlag struct{ t time.Time }

func (f *timeFlag) String() string {
	if f.t.IsZero() {
		return ""
	}
	return f.t.Format(time.RFC3339)
}

func (f *timeFlag) Set(s string) error {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Errorf("want RFC 3339 time: %w", err)
	}
	f.t = t.UTC()
	return nil
}
