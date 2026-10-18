package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/packages/events"
)

const reconcileViewConsumersTimeout = 2 * time.Minute

type viewConsumerFilterState struct {
	Exists       bool
	Filters      []string
	SingleFilter string
}

type reconcileViewConsumersOptions struct {
	storageConf, packageRoot, stream string
	timeout                          time.Duration
	yes, dryRun, restart             bool
}

type viewConsumerReconcileEntry struct {
	Consumer            string `json:"consumer"`
	Exists              bool   `json:"exists"`
	ExpectedFilterCount int    `json:"expected_filter_count"`
	ActualFilterCount   int    `json:"actual_filter_count"`
	FilterDrift         bool   `json:"filter_drift"`
	Reset               bool   `json:"reset"`
}

type viewConsumerReconcileSummary struct {
	Stream         string                       `json:"stream"`
	Entries        []viewConsumerReconcileEntry `json:"entries"`
	ResetConsumers []string                     `json:"reset_consumers,omitempty"`
	Restarted      bool                         `json:"restarted"`
	DryRun         bool                         `json:"dry_run"`
}

func runReconcileViewConsumers(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("reconcile-view-consumers", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := reconcileViewConsumersOptions{
		storageConf: defaultRepairStorageConfigPath(),
		stream:      defaultRepairJSName,
		timeout:     reconcileViewConsumersTimeout,
		restart:     true,
	}
	fs.StringVar(&opts.storageConf, "storage-conf", opts.storageConf, "storage business config path")
	fs.StringVar(&opts.packageRoot, "package-root", "", "storage package root containing start.sh/stop.sh")
	fs.StringVar(&opts.stream, "stream", opts.stream, "JetStream stream")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "overall operation timeout")
	fs.BoolVar(&opts.yes, "yes", false, "confirm deleting only consumers with filter drift")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "inspect consumer drift without changing state")
	fs.BoolVar(&opts.restart, "restart", opts.restart, "restart storage-view after reconciliation")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(stdout)
			fs.PrintDefaults()
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected reconcile-view-consumers arguments: %s", strings.Join(fs.Args(), " "))
	}
	if opts.timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	if strings.TrimSpace(opts.stream) == "" {
		return errors.New("--stream must not be empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	return reconcileViewConsumers(ctx, opts, stdout, stderr)
}

func reconcileViewConsumers(ctx context.Context, opts reconcileViewConsumersOptions, stdout, stderr io.Writer) error {
	storage, err := loadStorage(opts.storageConf)
	if err != nil {
		return err
	}
	desired, err := desiredStaticViewConsumerFilters(storage)
	if err != nil {
		return err
	}
	admin, err := openEventBusAdmin(opts.storageConf, resolveRepairPackageRoot(opts.packageRoot, opts.storageConf))
	if err != nil {
		return err
	}
	defer admin.Close()
	actual, err := inspectViewConsumerFilters(ctx, admin, opts.stream)
	if err != nil {
		return err
	}
	drift := viewConsumerFilterDrift(desired, actual)
	summary := viewConsumerReconcileSummary{Stream: opts.stream, DryRun: opts.dryRun}
	for consumer, filters := range desired {
		state := actual[consumer]
		summary.Entries = append(summary.Entries, viewConsumerReconcileEntry{
			Consumer: consumer, Exists: state.Exists, ExpectedFilterCount: len(filters),
			ActualFilterCount: len(state.Filters) + boolToInt(state.SingleFilter != ""), FilterDrift: drift[consumer],
			Reset: opts.yes && !opts.dryRun && drift[consumer],
		})
	}
	slices.SortFunc(summary.Entries, func(a, b viewConsumerReconcileEntry) int { return strings.Compare(a.Consumer, b.Consumer) })
	if opts.dryRun || len(drift) == 0 {
		status := "ok"
		if opts.dryRun {
			status = "dry_run"
		}
		return writeOperationResult(stdout, operationResult{Module: "storage", Action: "reconcile-view-consumers", Status: status, Summary: summary})
	}
	if !opts.yes {
		return fmt.Errorf("View consumer filter drift detected for %s; re-run with --yes or --dry-run", strings.Join(sortedConsumerNames(drift), ","))
	}
	packageRoot := resolveRepairPackageRoot(opts.packageRoot, opts.storageConf)
	if err := runStorageViewLifecycle(ctx, packageRoot, "stop", "new", stderr); err != nil {
		return fmt.Errorf("stop storage-view: %w", err)
	}
	stopped := true
	started := false
	defer func() {
		if stopped && opts.restart && !started {
			_ = runStorageViewLifecycle(context.Background(), packageRoot, "start", "new", stderr)
		}
	}()
	for _, consumer := range sortedConsumerNames(drift) {
		if _, _, err := admin.DeleteConsumers(ctx, opts.stream, []string{consumer}); err != nil {
			return fmt.Errorf("reset View consumer %s: %w", consumer, err)
		}
		summary.ResetConsumers = append(summary.ResetConsumers, consumer)
	}
	if opts.restart {
		if err := runStorageViewLifecycle(ctx, packageRoot, "start", "new", stderr); err != nil {
			return fmt.Errorf("start storage-view: %w", err)
		}
		started = true
		summary.Restarted = true
	}
	stopped = false
	return writeOperationResult(stdout, operationResult{Module: "storage", Action: "reconcile-view-consumers", Status: "ok", Summary: summary})
}

func desiredStaticViewConsumerFilters(storage storageconfig.StorageConfig) (map[string][]string, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	static := map[string]bool{
		events.StorageViewFactorConsumer:  true,
		events.StorageViewMetricsConsumer: true,
	}
	desired := make(map[string][]string)
	for _, partition := range storage.View.ConsumerPartitions {
		if !static[partition.Durable] {
			continue
		}
		for _, dataset := range partition.Datasets() {
			if dataset.DatasetID == "*" {
				return nil, fmt.Errorf("static View consumer %q must not use wildcard Dataset routes", partition.Durable)
			}
			for _, event := range events.StorageDatasetEvents {
				filter, err := registry.RenderSubject(event, dataset.SpaceID, dataset.DatasetID)
				if err != nil {
					return nil, fmt.Errorf("render View consumer %q filter: %w", partition.Durable, err)
				}
				desired[partition.Durable] = append(desired[partition.Durable], filter)
			}
		}
	}
	return desired, nil
}

// inspectViewConsumerFilters 读取两个静态 View 消费者当前的过滤主题。
func inspectViewConsumerFilters(ctx context.Context, admin eventBusAdmin, stream string) (map[string]viewConsumerFilterState, error) {
	durables := []string{events.StorageViewFactorConsumer, events.StorageViewMetricsConsumer}
	result, err := admin.Inspect(ctx, stream, durables)
	if err != nil {
		return nil, fmt.Errorf("inspect View consumers: %w", err)
	}
	return result, nil
}

func viewConsumerFilterDrift(desired map[string][]string, actual map[string]viewConsumerFilterState) map[string]bool {
	drift := make(map[string]bool)
	for consumer, expected := range desired {
		state, ok := actual[consumer]
		if !ok || !state.Exists || state.SingleFilter != "" || !equalFilterSubjects(state.Filters, expected) {
			drift[consumer] = true
		}
	}
	return drift
}

func equalFilterSubjects(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	counts := make(map[string]int, len(expected))
	for _, subject := range expected {
		counts[subject]++
	}
	for _, subject := range actual {
		counts[subject]--
		if counts[subject] < 0 {
			return false
		}
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func sortedConsumerNames(values map[string]bool) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
