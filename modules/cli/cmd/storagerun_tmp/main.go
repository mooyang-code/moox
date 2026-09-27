package main

import (
	"context"
	"fmt"
	"os"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

func main() {
	if len(os.Args) != 2 {
		panic("usage: storagerun_tmp <script>")
	}
	wd, _ := os.Getwd()
	snap, err := setupconfig.Load("./moox.toml", wd)
	if err != nil {
		panic(err)
	}
	h := snap.Manifest.StorageHost
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	c, err := setupssh.Dial(ctx, setupssh.Target{Name: h.Name, Address: h.Address, Port: h.Port, Username: h.Username}, h.Password, setupssh.Options{Timeout: 20 * time.Second})
	if err != nil {
		panic(err)
	}
	defer c.Close()
	r, err := c.Run(ctx, []string{"bash", "-lc", os.Args[1]}, nil)
	if r.Stdout != "" {
		fmt.Print(r.Stdout)
	}
	if r.Stderr != "" {
		fmt.Fprint(os.Stderr, r.Stderr)
	}
	if err != nil {
		panic(err)
	}
	if r.ExitCode != 0 {
		os.Exit(r.ExitCode)
	}
}
