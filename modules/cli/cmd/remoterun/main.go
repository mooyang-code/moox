package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: remoterun [--host <name>] <bash-script>")
		os.Exit(2)
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	snapshot, err := setupconfig.Load("./moox.toml", wd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	args := os.Args[1:]
	host := snapshot.Manifest.ControlHost
	if len(args) >= 2 && args[0] == "--host" {
		resolved, ok := hostByName(snapshot.Manifest, args[1])
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown host %q\n", args[1])
			os.Exit(2)
		}
		host = resolved
		args = args[2:]
	}
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: remoterun [--host <name>] <bash-script>")
		os.Exit(2)
	}
	script := strings.TrimSpace(args[0])
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	client, err := setupssh.Dial(ctx, setupssh.Target{
		Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username,
	}, host.Password, setupssh.Options{Timeout: 20 * time.Second})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer client.Close()
	result, err := client.Run(ctx, []string{"bash", "-lc", script}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if result.Stdout != "" {
		fmt.Print(result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}
	if result.ExitCode != 0 {
		os.Exit(result.ExitCode)
	}
}

func hostByName(manifest setupconfig.Manifest, name string) (setupconfig.Host, bool) {
	name = strings.TrimSpace(name)
	for _, host := range manifest.Hosts() {
		if strings.EqualFold(strings.TrimSpace(host.Name), name) {
			return host, true
		}
	}
	switch name {
	case "storage":
		return manifest.StorageHost, true
	case "control":
		return manifest.ControlHost, true
	default:
		return setupconfig.Host{}, false
	}
}
