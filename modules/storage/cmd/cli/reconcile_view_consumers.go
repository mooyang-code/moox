package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

const reconcileViewConsumersTimeout = 2 * time.Minute

type viewConsumerFilterState struct {
	Exists       bool
	Filters      []string
	SingleFilter string
}

type reconcileViewConsumersOptions struct {
	storageConf, packageRoot, stream, credentialFile, eventBusURL string
	timeout                                                       time.Duration
	yes, dryRun, restart                                          bool
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
	fs.StringVar(&opts.credentialFile, "credential-file", "", "NATS admin credential file")
	fs.StringVar(&opts.eventBusURL, "eventbus-url", "", "NATS URL override")
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
	actual, err := inspectViewConsumerFilters(ctx, opts)
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
	if err := runStorageViewLifecycle(ctx, packageRoot, "stop", "new", 0, stderr); err != nil {
		return fmt.Errorf("stop storage-view: %w", err)
	}
	stopped := true
	started := false
	defer func() {
		if stopped && opts.restart && !started {
			_ = runStorageViewLifecycle(context.Background(), packageRoot, "start", "new", 0, stderr)
		}
	}()
	for _, consumer := range sortedConsumerNames(drift) {
		if _, err := deleteJetStreamConsumer(ctx, repairConsumerOptions{Stream: opts.stream, Consumer: consumer, CredentialFile: opts.credentialFile, EventBusURL: opts.eventBusURL}); err != nil {
			return fmt.Errorf("reset View consumer %s: %w", consumer, err)
		}
		summary.ResetConsumers = append(summary.ResetConsumers, consumer)
	}
	if opts.restart {
		if err := runStorageViewLifecycle(ctx, packageRoot, "start", "new", 0, stderr); err != nil {
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
		events.StorageViewKlineConsumer:   true,
		events.StorageViewFactorConsumer:  true,
		events.StorageViewMetricsConsumer: true,
	}
	eventFamilies := []events.Event{events.DatasetRowsUpserted, events.DatasetPeriodCollected, events.FactorPeriodComputed, events.DatasetSyncPoint}
	desired := make(map[string][]string)
	for _, partition := range storage.View.ConsumerPartitions {
		if !static[partition.Durable] {
			continue
		}
		for _, dataset := range partition.Datasets() {
			if dataset.DatasetID == "*" {
				return nil, fmt.Errorf("static View consumer %q must not use wildcard Dataset routes", partition.Durable)
			}
			for _, event := range eventFamilies {
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

func inspectViewConsumerFilters(ctx context.Context, opts reconcileViewConsumersOptions) (map[string]viewConsumerFilterState, error) {
	credentialPath := resolveRepairCredentialFile(opts.credentialFile)
	if credentialPath == "" {
		return nil, errors.New("NATS admin credential is required; pass --credential-file or MOOX_STORAGE_EVENTBUS_ADMIN_CREDENTIAL_FILE")
	}
	cred, err := jetstream.LoadCredentialFile(credentialPath)
	if err != nil {
		return nil, err
	}
	urls := cred.URLs
	if strings.TrimSpace(opts.eventBusURL) != "" {
		urls = []string{strings.TrimSpace(opts.eventBusURL)}
	}
	if len(urls) == 0 {
		urls = []string{"tls://127.0.0.1:4222"}
	}
	natsOpts := []nats.Option{nats.Name("moox-storage-cli-reconcile-view-consumers"), nats.UserInfo(cred.Username, cred.Password), nats.Timeout(15 * time.Second)}
	if cred.CAFile != "" {
		caPath := jetstream.ExpandCredentialPath(cred.CAFile)
		if !filepath.IsAbs(caPath) {
			caPath = filepath.Join(filepath.Dir(credentialPath), caPath)
		}
		if err := appendNATSTLSOptions(&natsOpts, caPath); err != nil {
			return nil, err
		}
	}
	nc, err := nats.Connect(strings.Join(urls, ","), natsOpts...)
	if err != nil {
		return nil, err
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return nil, err
	}
	result := make(map[string]viewConsumerFilterState)
	for _, consumer := range []string{events.StorageViewKlineConsumer, events.StorageViewFactorConsumer, events.StorageViewMetricsConsumer} {
		info, err := js.ConsumerInfo(opts.stream, consumer, nats.Context(ctx))
		if errors.Is(err, nats.ErrConsumerNotFound) {
			result[consumer] = viewConsumerFilterState{}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect View consumer %s: %w", consumer, err)
		}
		result[consumer] = viewConsumerFilterState{Exists: true, Filters: append([]string(nil), info.Config.FilterSubjects...), SingleFilter: info.Config.FilterSubject}
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
