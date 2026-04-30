package main

import (
	"fmt"
	"os"
)

const version = "0.0.1-dev"

func main() {
	if len(os.Args) < 2 {
		runServer()
		return
	}

	switch os.Args[1] {
	case "server":
		runServer()
	case "relay":
		runRelay()
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\nusage: mydhcp [server|relay|version]\n", os.Args[1])
		os.Exit(1)
	}
}

func runServer() {
	fmt.Fprintln(os.Stderr, "not implemented")
	os.Exit(1)
}

func runRelay() {
	fmt.Fprintln(os.Stderr, "not implemented")
	os.Exit(1)
}
