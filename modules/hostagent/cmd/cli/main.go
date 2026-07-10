package main

import (
	"fmt"
	"github.com/mooyang-code/moox/modules/hostagent/internal/config"
	"github.com/mooyang-code/moox/modules/hostagent/internal/identity"
	"os"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] != "identity" {
		fmt.Fprintln(os.Stderr, "usage: moox-host-agent-cli identity")
		os.Exit(2)
	}
	cfg, err := config.Load("./config/app.yaml")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	f, err := identity.LoadOrCreate(cfg.IdentityPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(f.AgentID)
}
