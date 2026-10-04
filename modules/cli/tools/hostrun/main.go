package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: hostrun <host> run|upload|download -- <args>")
		os.Exit(2)
	}
	root := strings.TrimSpace(os.Getenv("MOOX_ROOT"))
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			fatal(err)
		}
		root, err = findRepoRoot(wd)
		if err != nil {
			fatal(err)
		}
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		fatal(err)
	}
	defer clearPasswords(snapshot)
	host, err := findHost(snapshot.Manifest, os.Args[1])
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	client, err := setupssh.Dial(ctx, setupssh.Target{
		Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username,
	}, host.Password, setupssh.Options{Timeout: 45 * time.Second})
	if err != nil {
		fatal(err)
	}
	defer client.Close()

	switch os.Args[2] {
	case "hosts":
		for _, host := range snapshot.Manifest.Hosts() {
			fmt.Printf("%s %s:%d user=%s\n", host.Name, host.Address, host.Port, host.Username)
		}
		return
	case "run":
		script := strings.Join(os.Args[3:], " ")
		result, err := client.Run(ctx, []string{"bash", "-lc", script}, nil)
		if result.Stdout != "" {
			fmt.Print(result.Stdout)
		}
		if result.Stderr != "" {
			fmt.Fprint(os.Stderr, result.Stderr)
		}
		if err != nil {
			fatal(err)
		}
		if result.ExitCode != 0 {
			os.Exit(result.ExitCode)
		}
	case "upload":
		if len(os.Args) != 5 {
			fatal(fmt.Errorf("usage: hostrun <host> upload <local> <remote>"))
		}
		raw, err := os.ReadFile(os.Args[3])
		if err != nil {
			fatal(err)
		}
		if err := client.Upload(ctx, bytes.NewReader(raw), int64(len(raw)), os.Args[4], 0o700); err != nil {
			fatal(err)
		}
	case "download":
		if len(os.Args) != 5 {
			fatal(fmt.Errorf("usage: hostrun <host> download <remote> <local>"))
		}
		quoted := "'" + strings.ReplaceAll(os.Args[3], "'", `'"'"'`) + "'"
		result, err := client.Run(ctx, []string{"bash", "-lc", "base64 < " + quoted}, nil)
		if err != nil {
			fatal(err)
		}
		if result.ExitCode != 0 {
			fmt.Fprint(os.Stderr, result.Stderr)
			os.Exit(result.ExitCode)
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(result.Stdout))
		if err != nil {
			fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(os.Args[4]), 0o700); err != nil {
			fatal(err)
		}
		if err := os.WriteFile(os.Args[4], raw, 0o600); err != nil {
			fatal(err)
		}
	default:
		fatal(fmt.Errorf("unknown command %s", os.Args[2]))
	}
	_ = io.Discard
}

func findRepoRoot(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "moox.toml")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("moox.toml not found from %s", start)
		}
		dir = parent
	}
}

func findHost(manifest setupconfig.Manifest, name string) (setupconfig.Host, error) {
	for _, host := range manifest.Hosts() {
		if strings.EqualFold(host.Name, strings.TrimSpace(name)) {
			return host, nil
		}
	}
	return setupconfig.Host{}, fmt.Errorf("setup_host_not_found: %s", name)
}

func clearPasswords(snapshot *setupconfig.Snapshot) {
	if snapshot == nil {
		return
	}
	snapshot.Manifest.ControlHost.Password = ""
	snapshot.Manifest.StorageHost.Password = ""
	snapshot.Manifest.ViewHost.Password = ""
	snapshot.Manifest.CompileHost.Password = ""
	for i := range snapshot.Manifest.OtherHosts {
		snapshot.Manifest.OtherHosts[i].Password = ""
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(1)
}
