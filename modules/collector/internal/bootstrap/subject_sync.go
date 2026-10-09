package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	"github.com/mooyang-code/moox/packages/timerjob"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	subjectTagsTimerService       = "trpc.moox.collector.subject_tags.timer"
	subjectAttributesTimerService = "trpc.moox.collector.subject_attributes.timer"
	subjectSyncTimeout            = 10 * time.Minute
)

func registerSubjectSyncRuntime(s *server.Server, cfg *Config, process *Runtime) error {
	http, err := httpclient.NewEgressHTTPClient(process.gateway, cfg.EgressProxy.Domains)
	if err != nil {
		return fmt.Errorf("initialize subject HTTP client: %w", err)
	}
	process.beforeClose = append(process.beforeClose, func() error { http.Close(); return nil })
	listers, err := marketwiring.NewSubjectListers(http)
	if err != nil {
		return fmt.Errorf("initialize subject listers: %w", err)
	}
	storage, err := subjectsync.NewStorageClient(process.gateway)
	if err != nil {
		return fmt.Errorf("initialize subject storage: %w", err)
	}
	for _, job := range cfg.SubjectSync.Attributes {
		kind := job.InstrumentType
		if kind == "" {
			kind = map[string]string{"crypto": "spot", "stockcn": "equity"}[job.SpaceID]
		}
		for _, source := range job.Sources {
			if listers[subjectsync.ListerKey{Source: source, InstrumentType: kind}] == nil {
				return fmt.Errorf("subject_sync source %s does not support %s subject listing", source, kind)
			}
		}
	}
	registrationCtx, cancel := context.WithTimeout(process.ctx, cfg.SubjectSync.FetchTimeout)
	defer cancel()
	if err := storage.RegisterSubjectListing(registrationCtx, listers.Supported()); err != nil {
		return fmt.Errorf("register subject listing: %w", err)
	}
	metrics := subjectsync.NewMetrics(prometheus.DefaultRegisterer)
	tags := &subjectsync.TagRunner{Store: storage, Listers: listers, FetchTimeout: cfg.SubjectSync.FetchTimeout, Metrics: metrics}
	attributes := &subjectsync.AttributeRunner{Store: storage, Listers: listers, Jobs: cfg.SubjectSync.Attributes, FetchTimeout: cfg.SubjectSync.FetchTimeout, Metrics: metrics}
	return registerSubjectSyncTimers(s, process, tags.RunOnce, attributes.RunOnce)
}

func subjectSyncJob(name string, process *Runtime, run func(context.Context) error) (*timerjob.Job, error) {
	if process == nil || run == nil {
		return nil, fmt.Errorf("subject sync timer requires runtime and runner")
	}
	return timerjob.New(name, subjectSyncTimeout, func(ctx context.Context) error {
		return process.run(ctx, run)
	})
}

func registerSubjectSyncTimers(s *server.Server, process *Runtime, tags, attributes func(context.Context) error) error {
	if s == nil {
		return fmt.Errorf("subject sync timers require a server")
	}
	for _, entry := range []struct {
		service string
		name    string
		run     func(context.Context) error
	}{
		{subjectTagsTimerService, "collector_subject_tags", tags},
		{subjectAttributesTimerService, "collector_subject_attributes", attributes},
	} {
		service := s.Service(entry.service)
		if service == nil {
			return fmt.Errorf("subject sync timer %q is not configured", entry.service)
		}
		job, err := subjectSyncJob(entry.name, process, entry.run)
		if err != nil {
			return err
		}
		timer.RegisterHandlerService(service, job.Handle)
	}
	return nil
}
