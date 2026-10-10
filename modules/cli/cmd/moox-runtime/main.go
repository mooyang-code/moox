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
	"runtime"
	"strings"
	"syscall"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbootstrap"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitinstall"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
)

var Version = "dev"

func main() {
	if len(os.Args) == 4 && os.Args[1] == "exec-component" {
		if err := unitruntime.RunComponentChild(os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		// Private file contents and child output never form part of errors.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "bootstrap" {
		flags := flag.NewFlagSet("moox-runtime bootstrap", flag.ContinueOnError)
		flags.SetOutput(stderr)
		filename := flags.String("request", "", "generated private bootstrap JSON request")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *filename == "" || flags.NArg() != 0 {
			return errors.New("bootstrap requires --request and no positional arguments")
		}
		request, err := unitbootstrap.ReadRequest(*filename)
		if err != nil {
			return err
		}
		result, err := unitbootstrap.Run(ctx, request)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(result)
	}
	if len(args) > 0 && (args[0] == "activate" || args[0] == "rollback" || args[0] == "recover") {
		return installOperation(ctx, args[0], args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "inspect-release" {
		flags := flag.NewFlagSet("moox-runtime inspect-release", flag.ContinueOnError)
		flags.SetOutput(stderr)
		directory := flags.String("directory", "", "physical prepared release directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *directory == "" || flags.NArg() != 0 {
			return errors.New("inspect-release requires --directory and no positional arguments")
		}
		prepared, err := unitinstall.ReadPrepared(ctx, *directory)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(prepared)
	}
	if len(args) > 0 && args[0] == "prepare" {
		return prepare(ctx, args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "seal-state" {
		return sealState(ctx, args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "inspect-bundle" {
		return inspectBundle(ctx, args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "extract" {
		return extract(ctx, args[1:], stdout, stderr)
	}
	if len(args) == 1 && args[0] == "version" {
		return json.NewEncoder(stdout).Encode(map[string]string{"version": Version, "catalog_sha256": unitruntime.CatalogSHA256()})
	}
	if len(args) == 0 {
		return errors.New("usage: moox-runtime start|stop|restart|pause|resume|healthcheck|status --plan PATH [--components ID,...]")
	}
	flags := flag.NewFlagSet("moox-runtime "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	var plan, components string
	var options unitruntime.Options
	flags.StringVar(&plan, "plan", "", "private runtime.json in a physical release or its current view")
	flags.StringVar(&components, "components", "", "deployed component IDs; defaults to the entire unit")
	flags.BoolVar(&options.MaintenanceLockHeld, "maintenance-lock-held", false, "reuse the caller's inherited exclusive maintenance lock")
	flags.IntVar(&options.MaintenanceLockFD, "maintenance-lock-fd", 3, "inherited maintenance lock descriptor")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || plan == "" {
		return errors.New("runtime requires --plan and no positional arguments")
	}
	var ids []string
	if components != "" {
		ids = strings.Split(components, ",")
	}
	result, err := unitruntime.Execute(ctx, plan, args[0], ids, options)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}

func installOperation(ctx context.Context, operation string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("moox-runtime "+operation, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options unitinstall.ActivateOptions
	var lock unitruntime.Options
	var unitRoot, components, seedDirectory, seedSHA256 string
	if operation == "activate" {
		flags.StringVar(&options.Directory, "directory", "", "physical prepared release")
		flags.BoolVar(&options.NoStart, "no-start", false, "switch current without starting components")
		flags.StringVar(&components, "components", "", "component start selection")
		flags.StringVar(&seedDirectory, "state-seed", "", "sealed private offline state directory")
		flags.StringVar(&seedSHA256, "state-seed-sha256", "", "state producer's receipt digest")
	} else {
		flags.StringVar(&unitRoot, "unit-root", "", "physical deployment unit root")
	}
	flags.BoolVar(&lock.MaintenanceLockHeld, "maintenance-lock-held", false, "reuse a verified inherited maintenance lock")
	flags.IntVar(&lock.MaintenanceLockFD, "maintenance-lock-fd", 3, "inherited maintenance descriptor")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (operation == "activate" && options.Directory == "") || (operation != "activate" && unitRoot == "") {
		return errors.New("installation operation requires its directory/root and no positional arguments")
	}
	if components != "" {
		options.Components = strings.Split(components, ",")
	}
	if seedDirectory != "" || seedSHA256 != "" {
		options.StateSeed = &unitinstall.StateSeedReference{Directory: seedDirectory, SHA256: seedSHA256}
	}
	var result unitinstall.Activation
	var err error
	switch operation {
	case "activate":
		result, err = unitinstall.Activate(ctx, options, lock)
	case "rollback":
		result, err = unitinstall.Rollback(ctx, unitRoot, lock)
	default:
		result, err = unitinstall.Recover(ctx, unitRoot, lock)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}

func sealState(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("moox-runtime seal-state", flag.ContinueOnError)
	flags.SetOutput(stderr)
	request := flags.String("request", "", "generated private offline state request")
	var lock unitruntime.Options
	flags.BoolVar(&lock.MaintenanceLockHeld, "maintenance-lock-held", false, "reuse a verified inherited maintenance lock")
	flags.IntVar(&lock.MaintenanceLockFD, "maintenance-lock-fd", 3, "inherited maintenance descriptor")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *request == "" || flags.NArg() != 0 {
		return errors.New("seal-state requires --request and no positional arguments")
	}
	options, err := unitinstall.ReadSealStateRequest(*request)
	if err != nil {
		return err
	}
	result, err := unitinstall.SealState(ctx, options, lock)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}

func prepare(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("moox-runtime prepare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var request string
	var lock unitruntime.Options
	flags.StringVar(&request, "request", "", "generated private preparation request")
	flags.BoolVar(&lock.MaintenanceLockHeld, "maintenance-lock-held", false, "reuse a verified inherited maintenance lock")
	flags.IntVar(&lock.MaintenanceLockFD, "maintenance-lock-fd", 3, "inherited maintenance descriptor")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if request == "" || flags.NArg() != 0 {
		return errors.New("prepare requires --request and no positional arguments")
	}
	options, err := unitinstall.ReadPrepareRequest(request)
	if err != nil {
		return err
	}
	result, err := unitinstall.Prepare(ctx, options, lock)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}

func inspectBundle(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("moox-runtime inspect-bundle", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options unitbundle.Options
	var directory, components string
	flags.StringVar(&directory, "directory", "", "physical private identity directory")
	flags.StringVar(&options.HostID, "host-id", "", "canonical target host")
	flags.StringVar(&options.ControlHostID, "control-host-id", "", "canonical control host")
	flags.StringVar(&options.Address, "address", "", "target public address from registered topology")
	flags.StringVar(&options.PrivateAddress, "private-address", "", "target private address from registered topology")
	flags.StringVar(&options.ControlAddress, "control-address", "", "registered control address")
	flags.StringVar(&options.ExpectedCA, "ca-sha256", "", "pinned DER CA fingerprint")
	flags.StringVar(&options.ExpectedHash, "snapshot-sha256", "", "expected compiled snapshot hash")
	flags.StringVar(&components, "components", "", "complete target placement, comma-separated")
	flags.BoolVar(&options.AllowOperator, "bootstrap-operator", false, "expect a control bootstrap operator identity")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || directory == "" {
		return errors.New("inspect-bundle requires --directory, topology and pinned CA/snapshot, with no positional arguments")
	}
	if components != "" {
		options.Components = strings.Split(components, ",")
	}
	material, err := unitbundle.Load(ctx, directory, options)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(material.Metadata())
}

func extract(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("moox-runtime extract", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := unitpackage.ExtractOptions{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	flags.StringVar(&options.Archive, "archive", "", "uploaded software .tar.gz")
	flags.StringVar(&options.ExpectedSHA256, "sha256", "", "digest from the package producer")
	flags.StringVar(&options.Profile, "profile", "", "host/control/storage/access/egress-proxy/trade")
	flags.StringVar(&options.Destination, "destination", "", "new physical release directory; never overwritten")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || options.Archive == "" {
		return errors.New("extract requires --archive, --sha256, --profile and --destination, with no positional arguments")
	}
	result, err := unitpackage.Extract(ctx, options)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}
