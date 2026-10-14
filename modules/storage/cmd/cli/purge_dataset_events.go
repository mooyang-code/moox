package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/mooyang-code/moox/packages/events"
)

type purgeDatasetEventsOptions struct {
	stream, spaceID, datasetID, storageConf, packageRoot string
	yes, dryRun                                          bool
}

var purgeDatasetEventSubjects = purgeDatasetEventSubjectsFromJetStream

func runPurgeDatasetEvents(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("purge-dataset-events", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := purgeDatasetEventsOptions{stream: "MOOX_STORAGE"}
	fs.StringVar(&opts.stream, "stream", opts.stream, "JetStream stream")
	fs.StringVar(&opts.spaceID, "space", "", "exact space ID")
	fs.StringVar(&opts.datasetID, "dataset", "", "exact Dataset ID")
	fs.StringVar(&opts.storageConf, "storage-conf", defaultRepairStorageConfigPath(), "storage-view 的 storage.yaml 路径（用其中的网关身份访问 Admin）")
	fs.StringVar(&opts.packageRoot, "package-root", "", "storage 包根目录")
	fs.BoolVar(&opts.yes, "yes", false, "confirm permanent subject purge")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "print exact subjects without purging")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected purge-dataset-events arguments: %s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(opts.spaceID) == "" || strings.TrimSpace(opts.datasetID) == "" {
		return errors.New("--space and --dataset are required")
	}
	subjects, err := datasetEventSubjects(opts.spaceID, opts.datasetID)
	if err != nil {
		return err
	}
	if !opts.dryRun && !opts.yes {
		return errors.New("purge-dataset-events permanently removes matching stream messages; re-run with --yes, or use --dry-run")
	}
	if !opts.dryRun {
		if err := purgeDatasetEventSubjects(ctx, opts, subjects); err != nil {
			return err
		}
	}
	status := "ok"
	if opts.dryRun {
		status = "dry_run"
	}
	return json.NewEncoder(stdout).Encode(operationResult{Module: "storage", Action: "purge-dataset-events", Status: status, Summary: map[string]any{
		"stream": opts.stream, "space_id": opts.spaceID, "dataset_id": opts.datasetID, "subjects": subjects,
	}})
}

func datasetEventSubjects(spaceID, datasetID string) ([]string, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	subjects := make([]string, 0, len(events.StorageDatasetEvents))
	for _, eventType := range events.StorageDatasetEvents {
		subject, renderErr := registry.RenderSubject(eventType, strings.TrimSpace(spaceID), strings.TrimSpace(datasetID))
		if renderErr != nil {
			return nil, renderErr
		}
		subjects = append(subjects, subject)
	}
	return subjects, nil
}

func purgeDatasetEventSubjectsFromJetStream(ctx context.Context, opts purgeDatasetEventsOptions, subjects []string) error {
	admin, err := openEventBusAdmin(opts.storageConf, resolveRepairPackageRoot(opts.packageRoot, opts.storageConf))
	if err != nil {
		return err
	}
	defer admin.Close()
	if err := admin.PurgeSubjects(ctx, opts.stream, subjects); err != nil {
		return fmt.Errorf("purge subjects: %w", err)
	}
	return nil
}
