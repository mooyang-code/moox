package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
)

func TestRunOnceSuccessPayloadKeepsRunIDCompatibility(t *testing.T) {
	task := &engine.FactorTask{TaskID: "manual-123"}

	payload := runOncePayload(task, domain.RunStatusSucceeded, 2, 7)

	if payload["task_id"] != "manual-123" {
		t.Fatalf("task_id = %v", payload["task_id"])
	}
	if payload["run_id"] != "manual-123-succeeded" {
		t.Fatalf("run_id = %v", payload["run_id"])
	}
}

func TestParseInitArgs(t *testing.T) {
	cfg, err := parseArgs([]string{"init", "--db", "./tmp/factor.db"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if cfg.Command != "init" || cfg.DBPath != "./tmp/factor.db" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseImportArgs(t *testing.T) {
	cfg, err := parseArgs([]string{"import", "--db", "./tmp/factor.db", "--factors-dir", "./factors", "--default-params", "20,96,288"})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if cfg.Command != "import" || cfg.FactorsDir != "./factors" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.DefaultParams, []int{20, 96, 288}) {
		t.Fatalf("default params = %#v", cfg.DefaultParams)
	}
}

func TestParseRunOnceArgs(t *testing.T) {
	cfg, err := parseArgs([]string{
		"run-once",
		"--space", "crypto",
		"--dataset", "binance_spot_kline",
		"--subject", "BTC-USDT",
		"--freq", "1m",
		"--bar-time", "2026-07-06T09:15:00Z",
		"--factors", "bias,cci",
	})
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if cfg.Command != "run-once" || cfg.SpaceID != "crypto" || cfg.DatasetID != "binance_spot_kline" || cfg.SubjectID != "BTC-USDT" || cfg.Freq != "1m" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.BarTime != time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC) {
		t.Fatalf("bar time = %s", cfg.BarTime)
	}
	if !reflect.DeepEqual(cfg.FactorIDs, []string{"bias", "cci"}) {
		t.Fatalf("factor ids = %#v", cfg.FactorIDs)
	}
}
