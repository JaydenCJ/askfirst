// Command askfirst is the approval queue for agent actions: agents submit
// what they want to do, policy rules auto-approve the safe requests, and
// everything else waits in a local inbox for a human decision.
package main

import (
	"os"

	"github.com/JaydenCJ/askfirst/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
