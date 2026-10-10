package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/mooyang-code/moox/modules/consoleproxy/internal/bootstrap"
	"github.com/mooyang-code/moox/modules/consoleproxy/internal/config"
	"github.com/mooyang-code/moox/modules/consoleproxy/internal/engine"
)

var Version = "dev"
var GitCommit = "unknown"
var BuildTime = "unknown"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "console-proxy:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: moox-console-proxy serve|validate|check-state|version|stop-budget [--config path]")
	}
	if args[0] == "version" {
		if len(args) != 1 {
			return errors.New("version does not accept arguments")
		}
		caddyVersion := "unknown"
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, dep := range info.Deps {
				if dep.Path == "github.com/caddyserver/caddy/v2" {
					caddyVersion = dep.Version
				}
			}
		}
		return json.NewEncoder(out).Encode(map[string]string{"component": "console-proxy", "version": Version, "git_commit": GitCommit, "build_time": BuildTime, "caddy_version": caddyVersion})
	}
	if args[0] != "serve" && args[0] != "validate" && args[0] != "check-state" && args[0] != "stop-budget" {
		return fmt.Errorf("unknown command %q", args[0])
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "config/console-proxy.yaml", "component configuration")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	switch args[0] {
	case "check-state":
		fingerprint, err := engine.CheckState(cfg)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"ca_sha256": fingerprint})
	case "validate":
		if _, err := engine.Render(cfg); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "console-proxy configuration valid")
		return err
	case "stop-budget":
		_, err := fmt.Fprintln(out, int64((cfg.StopBudget()+time.Second-1)/time.Second))
		return err
	default:
		return bootstrap.Serve(ctx, cfg, Version, GitCommit)
	}
}
