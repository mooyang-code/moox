package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
)

func runRecalc(ctx context.Context, cfg cliConfig, out io.Writer) error {
	if cfg.RequestID == "" || cfg.SpaceID == "" || cfg.SubjectID == "" || cfg.Freq == "" {
		return fmt.Errorf("--request-id, --space, --subject and --freq are required")
	}
	sourceViewID := cfg.ViewID
	if sourceViewID == "" {
		sourceViewID = cfg.DatasetID
	}
	if sourceViewID == "" {
		return fmt.Errorf("--view-id or --dataset is required")
	}
	db, err := openControlStore(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	svc, err := trigger.NewRecalcService(db, nil)
	if err != nil {
		return err
	}
	job, err := svc.Accept(ctx, trigger.RecalcSpec{
		RequestID: cfg.RequestID, SpaceID: cfg.SpaceID, DatasetID: cfg.DatasetID,
		SourceViewID: sourceViewID, SubjectID: cfg.SubjectID, Frequency: cfg.Freq,
		FactorID: cfg.FactorID, StartTime: cfg.StartTime, EndTime: cfg.EndTime,
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"ok": true, "job_id": job.JobID, "status": job.Status,
	})
}

func runRecalcCancel(ctx context.Context, cfg cliConfig, out io.Writer) error {
	if cfg.JobID == "" {
		return fmt.Errorf("--job-id is required")
	}
	db, err := openControlStore(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	svc, err := trigger.NewRecalcService(db, nil)
	if err != nil {
		return err
	}
	if err := svc.Cancel(ctx, cfg.JobID); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"ok": true, "job_id": cfg.JobID, "status": trigger.RecalcCancelled})
}

func runRecalcStatus(ctx context.Context, cfg cliConfig, out io.Writer) error {
	if cfg.JobID == "" {
		return fmt.Errorf("--job-id is required")
	}
	db, err := openControlStore(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	svc, err := trigger.NewRecalcService(db, nil)
	if err != nil {
		return err
	}
	job, err := svc.Get(ctx, cfg.JobID)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"ok": true, "job_id": job.JobID, "status": job.Status,
		"failure_class": job.FailureClass, "error": job.Error,
		"binding_generation": job.BindingGeneration,
	})
}

func openControlStore(path string) (*store.Store, error) {
	db, err := store.Open(&store.Options{Path: path})
	if err != nil {
		return nil, err
	}
	if err := db.ApplySchema(factorschema.AllSQL()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply factor schema: %w", err)
	}
	return db, nil
}
