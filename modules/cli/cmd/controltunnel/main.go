package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

func main() {
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
	host := snapshot.Manifest.ControlHost
	remote := getenvDefault("MOOX_TUNNEL_REMOTE", "127.0.0.1:11002")
	ready := getenvDefault("MOOX_TUNNEL_READY", "/tmp/moox-tunnel.ready")
	_ = os.Remove(ready)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, err := setupssh.Dial(ctx, setupssh.Target{
		Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username,
	}, host.Password, setupssh.Options{Timeout: 20 * time.Second})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer client.Close()

	listener, err := client.ForwardLocal(ctx, remote)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer listener.Close()
	if err := os.WriteFile(ready, []byte(listener.Addr().String()+"\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("tunnel %s -> %s\n", listener.Addr().String(), remote)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	<-signals
}

func getenvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
