// Command diagd is a network test server for CPE diagnostics: it implements
// the server side of Broadband Forum TR-143 (throughput and UDP echo tests)
// and TR-471 (maximum IP-layer capacity measurement).
package main

import (
	"fmt"
	"io"
	"os"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "serve":
		return cmdServe(args[1:])
	case "version":
		fmt.Println("diagd", version)
		return 0
	case "help", "-h", "--help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "diagd: unknown command %q\n\n", args[0])
		usage(os.Stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: diagd <command> [flags]

Commands:
  serve    run the diagnostics test server
  version  print the version

Run "diagd <command> -h" for command flags.
`)
}
