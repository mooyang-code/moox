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
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitwatch"
	"github.com/mooyang-code/moox/packages/hostpolicy"
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
	if len(args) > 0 && args[0] == "export-host" {
		flags := flag.NewFlagSet("moox-runtime export-host", flag.ContinueOnError)
		flags.SetOutput(stderr)
		filename := flags.String("request", "", "generated private host export JSON request")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *filename == "" || flags.NArg() != 0 {
			return errors.New("export-host requires --request and no positional arguments")
		}
		request, err := unitbootstrap.ReadExportRequest(*filename)
		if err != nil {
			return err
		}
		result, err := unitbootstrap.ExportHost(ctx, request)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(result)
	}
	if len(args) > 0 && args[0] == "deploy" {
		flags := flag.NewFlagSet("moox-runtime deploy", flag.ContinueOnError)
		flags.SetOutput(stderr)
		filename := flags.String("request", "", "generated private deployment JSON request")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *filename == "" || flags.NArg() != 0 {
			return errors.New("deploy requires --request and no positional arguments")
		}
		request, err := unitinstall.ReadPrepareRequest(*filename)
		if err != nil {
			return err
		}
		result, err := unitinstall.Deploy(ctx, request)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(result)
	}
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
	if len(args) > 0 && args[0] == "reseal" {
		// 把单元当前发布的文件清单重新封存为现状（原地换过二进制或改过配置之后）；不带 --yes 只列出差异路径。
		flags := flag.NewFlagSet("moox-runtime reseal", flag.ContinueOnError)
		flags.SetOutput(stderr)
		unitRoot := flags.String("unit-root", "", "physical deployment unit root, e.g. /data/moox/storage")
		yes := flags.Bool("yes", false, "rewrite prepared.json under the host maintenance lock")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *unitRoot == "" || flags.NArg() != 0 {
			return errors.New("reseal requires --unit-root and no positional arguments")
		}
		result, err := unitinstall.Reseal(ctx, *unitRoot, *yes, unitruntime.Options{}, nil)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(result)
	}
	if len(args) > 0 && args[0] == "watch" {
		return watch(ctx, args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "install-watchdog" {
		return installWatchdog(args[1:], os.Stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "host-policy-status" {
		// 只输出主机策略缓存的版本、来源与拉取时间，不含推送地址。
		flags := flag.NewFlagSet("moox-runtime host-policy-status", flag.ContinueOnError)
		flags.SetOutput(stderr)
		root := flags.String("root", "", "deployment root, e.g. /data/moox")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *root == "" || flags.NArg() != 0 {
			return errors.New("host-policy-status requires --root and no positional arguments")
		}
		return json.NewEncoder(stdout).Encode(hostpolicy.Summarize(hostpolicy.Path(*root)))
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

// watch 是 crontab 每分钟调用的主机级守护：遍历部署根下全部单元，按依赖顺序执行 healthcheck，输出一行 JSON 汇总。
func watch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("moox-runtime watch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options unitwatch.Options
	flags.StringVar(&options.Root, "root", "", "deployment root, e.g. /data/moox")
	flags.BoolVar(&options.DryRun, "dry-run", false, "report component states without starting or restarting anything")
	logPath := flags.String("log", "", "append the JSON report to this rotated log instead of stdout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if options.Root == "" || flags.NArg() != 0 {
		return errors.New("watch requires --root and no positional arguments")
	}
	if *logPath != "" {
		file, err := unitwatch.OpenLog(*logPath)
		if err != nil {
			return err
		}
		defer file.Close()
		stdout = file
	}
	report, err := unitwatch.Run(ctx, options)
	if encodeErr := json.NewEncoder(stdout).Encode(report); encodeErr != nil && err == nil {
		err = encodeErr
	}
	return err
}

// installWatchdog 幂等地写入部署用户 crontab 里的 watch 条目（--remove 删除），并可从 --seed-policy 写入策略种子；
// 种子含告警推送地址，用 "-" 从标准输入读取，不出现在命令行参数里。
func installWatchdog(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("moox-runtime install-watchdog", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options unitwatch.InstallOptions
	flags.StringVar(&options.Root, "root", "", "deployment root, e.g. /data/moox")
	flags.StringVar(&options.Runtime, "runtime", "", "moox-runtime used by the crontab entry (default <root>/host/current/bin/moox-runtime)")
	seed := flags.String("seed-policy", "", "host policy seed JSON file, or - for stdin")
	flags.BoolVar(&options.Remove, "remove", false, "remove the watchdog crontab entry")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if options.Root == "" || flags.NArg() != 0 || (options.Remove && *seed != "") {
		return errors.New("install-watchdog requires --root, accepts --seed-policy only when installing, and takes no positional arguments")
	}
	if *seed != "" {
		var raw []byte
		var err error
		if *seed == "-" {
			raw, err = io.ReadAll(io.LimitReader(stdin, 1<<20))
		} else {
			raw, err = os.ReadFile(*seed)
		}
		if err != nil {
			return err
		}
		var policy hostpolicy.Policy
		if err := json.Unmarshal(raw, &policy); err != nil {
			return fmt.Errorf("parse seed policy: %w", err)
		}
		policy.Source = hostpolicy.SourceSeed
		if err := policy.Validate(); err != nil {
			return err
		}
		options.Seed = &policy
	}
	result, err := unitwatch.Install(options)
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
	flags.BoolVar(&options.Outbound, "outbound", false, "target is an outbound-only host whose gateway certificate also allows client auth")
	flags.StringVar(&options.ExpectedCA, "ca-sha256", "", "pinned DER CA fingerprint")
	flags.StringVar(&options.ExpectedHash, "snapshot-sha256", "", "expected compiled snapshot hash")
	flags.StringVar(&components, "components", "", "complete target deployment, comma-separated")
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
