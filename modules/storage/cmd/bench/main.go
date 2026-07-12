package main

import (
	"fmt"
	"os"

	trpc "trpc.group/trpc-go/trpc-go"
)

func main() {
	if err := run(trpc.BackgroundContext()); err != nil {
		fmt.Fprintf(os.Stderr, "moox-storage-bench failed: %v\n", err)
		os.Exit(1)
	}
}
