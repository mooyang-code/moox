package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

type purgeDatasetEventsOptions struct {
	stream, spaceID, datasetID, credentialFile, eventBusURL string
	yes, dryRun                                             bool
}

var purgeDatasetEventSubjects = purgeDatasetEventSubjectsFromJetStream

func runPurgeDatasetEvents(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("purge-dataset-events", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := purgeDatasetEventsOptions{stream: "MOOX_STORAGE"}
	fs.StringVar(&opts.stream, "stream", opts.stream, "JetStream stream")
	fs.StringVar(&opts.spaceID, "space", "", "exact space ID")
	fs.StringVar(&opts.datasetID, "dataset", "", "exact Dataset ID")
	fs.StringVar(&opts.credentialFile, "credential-file", "", "NATS admin credential YAML")
	fs.StringVar(&opts.eventBusURL, "eventbus-url", "", "override EventBus URL")
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
	eventTypes := []events.Event{
		events.DatasetRowsUpserted,
		events.CollectorPeriodCompleted,
		events.FactorPeriodComputed,
		events.DatasetSyncPoint,
	}
	subjects := make([]string, 0, len(eventTypes))
	for _, eventType := range eventTypes {
		subject, renderErr := registry.RenderSubject(eventType, strings.TrimSpace(spaceID), strings.TrimSpace(datasetID))
		if renderErr != nil {
			return nil, renderErr
		}
		subjects = append(subjects, subject)
	}
	return subjects, nil
}

func purgeDatasetEventSubjectsFromJetStream(ctx context.Context, opts purgeDatasetEventsOptions, subjects []string) error {
	credentialPath := resolveRepairCredentialFile(opts.credentialFile)
	if credentialPath == "" {
		return errors.New("NATS admin credential is required; pass --credential-file or MOOX_STORAGE_EVENTBUS_ADMIN_CREDENTIAL_FILE")
	}
	cred, err := jetstream.LoadCredentialFile(credentialPath)
	if err != nil {
		return err
	}
	urls := cred.URLs
	if strings.TrimSpace(opts.eventBusURL) != "" {
		urls = []string{strings.TrimSpace(opts.eventBusURL)}
	}
	if err := validatePurgeEventBusURLs(urls, cred.CAFile); err != nil {
		return err
	}
	natsOpts := []nats.Option{nats.Name("moox-storage-cli-purge-dataset-events"), nats.UserInfo(cred.Username, cred.Password), nats.Timeout(15 * time.Second)}
	if cred.CAFile != "" {
		caPath := jetstream.ExpandCredentialPath(cred.CAFile)
		if !filepath.IsAbs(caPath) {
			caPath = filepath.Join(filepath.Dir(credentialPath), caPath)
		}
		if err := appendNATSTLSOptions(&natsOpts, caPath); err != nil {
			return err
		}
	}
	nc, err := connectAdminEventBus(urls, credentialPath, natsOpts...)
	if err != nil {
		return err
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	for _, subject := range subjects {
		if err := js.PurgeStream(opts.stream, &nats.StreamPurgeRequest{Subject: subject}, nats.Context(ctx)); err != nil {
			return fmt.Errorf("purge subject %s: %w", subject, err)
		}
	}
	return nil
}

// connectAdminEventBus 用管理员凭据连接 EventBus；失败时说明用的是哪份凭据文件和哪些地址。
// 管理员凭据里的地址通常是控制机回环地址（tls://127.0.0.1:4222），在 Storage 等其他主机上用必须用 --eventbus-url 指向 EventBus
// 的实际地址；默认路径（~/.config/moox/eventbus/internal-admin.yaml）上留着旧环境的凭据时也会得到同样的“连不上”，
// 光看 nats 的原始报错分辨不出是哪一种。
func connectAdminEventBus(urls []string, credentialPath string, opts ...nats.Option) (*nats.Conn, error) {
	nc, err := nats.Connect(strings.Join(urls, ","), opts...)
	if err != nil {
		return nil, fmt.Errorf("连接 EventBus %s 失败（凭据文件 %s）：%w；管理员凭据里的地址通常是控制机回环地址，在其他主机上要用 --eventbus-url 指定 EventBus 的实际地址", strings.Join(urls, ","), credentialPath, err)
	}
	return nc, nil
}

func validatePurgeEventBusURLs(urls []string, caFile string) error {
	if len(urls) == 0 {
		return errors.New("EventBus URL is required")
	}
	for _, raw := range urls {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("invalid EventBus URL %q", raw)
		}
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
		if !loopback && parsed.Scheme != "tls" {
			return fmt.Errorf("non-loopback EventBus URL %q must use tls", raw)
		}
		if !loopback && strings.TrimSpace(caFile) == "" {
			return fmt.Errorf("non-loopback EventBus URL %q requires a CA file", raw)
		}
	}
	return nil
}
