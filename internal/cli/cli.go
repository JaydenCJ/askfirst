// Package cli implements the askfirst command-line interface. All
// commands run in-process (no os.Exit inside Run) so integration tests
// can call them directly and assert on output and exit codes.
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/JaydenCJ/askfirst/internal/policy"
	"github.com/JaydenCJ/askfirst/internal/queue"
	"github.com/JaydenCJ/askfirst/internal/version"
)

// Exit codes. Submit and `policy test` map decisions onto them so shell
// wrappers can gate execution with a plain `if askfirst submit …; then`.
const (
	ExitOK      = 0 // approved, or the command simply succeeded
	ExitDenied  = 1 // the action was denied (by policy or a human)
	ExitUsage   = 2 // bad flags or arguments
	ExitRuntime = 3 // I/O or state errors
	ExitPending = 4 // still pending: queued, timed out, or expired
)

const usageText = `askfirst — approval queue for agent actions

usage:
  askfirst [--dir DIR] <command> [flags]

commands:
  init                 create the inbox directory and a starter policy
  submit               submit an action for approval (agents call this)
  list                 show the inbox (default: pending requests)
  show <id>            print one request in full
  approve <id>         approve a pending request
  deny <id>            deny a pending request
  log                  print the audit trail
  policy check         validate the policy file
  policy test          dry-run a decision and show the matching rule
  serve                run the local HTTP API (default 127.0.0.1:2750)
  version              print the version

the inbox directory is --dir, else $ASKFIRST_DIR, else ~/.askfirst
exit codes: 0 approved/ok · 1 denied · 2 usage · 3 runtime · 4 pending
`

// ctx carries what every command needs.
type ctx struct {
	dir string
	out io.Writer
	err io.Writer
}

// Run executes one CLI invocation and returns its exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	c := &ctx{out: stdout, err: stderr}

	// Peel global flags off the front so `askfirst --dir X list` works.
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--dir":
			if len(args) < 2 {
				return c.usageError("--dir needs a value")
			}
			c.dir = args[1]
			args = args[2:]
		case "--version":
			fmt.Fprintf(stdout, "askfirst %s\n", version.Version)
			return ExitOK
		case "-h", "--help", "-help":
			fmt.Fprint(stdout, usageText)
			return ExitOK
		default:
			return c.usageError("unknown flag %s", args[0])
		}
	}
	if c.dir == "" {
		c.dir = os.Getenv("ASKFIRST_DIR")
	}
	if c.dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return c.runtimeError("cannot resolve home directory: %v (pass --dir)", err)
		}
		c.dir = filepath.Join(home, ".askfirst")
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return ExitUsage
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "init":
		return c.cmdInit(rest)
	case "submit":
		return c.cmdSubmit(rest)
	case "list":
		return c.cmdList(rest)
	case "show":
		return c.cmdShow(rest)
	case "approve":
		return c.cmdDecide(rest, true)
	case "deny":
		return c.cmdDecide(rest, false)
	case "log":
		return c.cmdLog(rest)
	case "policy":
		return c.cmdPolicy(rest)
	case "serve":
		return c.cmdServe(rest)
	case "version":
		fmt.Fprintf(stdout, "askfirst %s\n", version.Version)
		return ExitOK
	case "help":
		fmt.Fprint(stdout, usageText)
		return ExitOK
	}
	return c.usageError("unknown command %q (see askfirst --help)", cmd)
}

func (c *ctx) usageError(format string, a ...any) int {
	fmt.Fprintf(c.err, "askfirst: "+format+"\n", a...)
	return ExitUsage
}

func (c *ctx) runtimeError(format string, a ...any) int {
	fmt.Fprintf(c.err, "askfirst: "+format+"\n", a...)
	return ExitRuntime
}

// newFlagSet builds a subcommand flag set that reports errors on stderr
// without exiting the process.
func (c *ctx) newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(c.err)
	return fs
}

// store opens the queue at the resolved inbox directory.
func (c *ctx) store() *queue.Store { return queue.Open(c.dir) }

// loadPolicy parses the policy file, honoring a per-command override.
func (c *ctx) loadPolicy(override string) (*policy.Policy, error) {
	path := override
	if path == "" {
		path = c.store().PolicyPath()
	}
	pol, err := policy.ParseFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no policy at %s (run `askfirst init` first)", path)
		}
		return nil, err
	}
	return pol, nil
}

// takeID peels a leading positional ID off the argument list, so commands
// read naturally as `askfirst approve af-… --reason ok`.
func takeID(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

// exitFor maps a request's final status onto the CLI exit-code contract.
func exitFor(status queue.Status) int {
	switch status {
	case queue.StatusApproved:
		return ExitOK
	case queue.StatusDenied:
		return ExitDenied
	default: // pending or expired
		return ExitPending
	}
}
