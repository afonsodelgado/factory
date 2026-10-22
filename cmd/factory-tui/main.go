package main

import (
	"fmt"
	"io"
	"os"
)

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
	case "--version", "version":
		fmt.Println(version)
		return 0
	case "-h", "--help", "help":
		usage(os.Stdout)
		return 0
	}
	fmt.Fprintf(os.Stderr, "factory-tui: unknown command %q\n\n", args[0])
	usage(os.Stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: factory-tui <command> [arguments]

commands:
  --version   print the version
`)
}
