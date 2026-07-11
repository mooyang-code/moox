package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: strategy validate|run-once")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "validate", "run-once":
		fmt.Println("ok")
	default:
		fmt.Fprintln(os.Stderr, "unknown command")
		os.Exit(2)
	}
}
