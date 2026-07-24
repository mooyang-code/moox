package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/encoding/protojson"
)

// replayJSONEvent is the stable offline input shape for `go run ./cmd/cli replay`.
// Each line contains one DatasetRowsUpserted protobuf in protojson form.
type replayJSONEvent struct {
	MessageID  string          `json:"message_id"`
	ReceivedAt string          `json:"received_at"`
	Event      json.RawMessage `json:"event"`
}

type fileReplaySource struct{ path string }

func (s fileReplaySource) Load(ctx context.Context, req trigger.ReplayRequest) ([]trigger.ReplayEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open replay input: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	out := make([]trigger.ReplayEvent, 0)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var raw replayJSONEvent
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			return nil, fmt.Errorf("decode replay input line %d: %w", lineNo, err)
		}
		receivedAt, err := time.Parse(time.RFC3339Nano, raw.ReceivedAt)
		if err != nil {
			return nil, fmt.Errorf("parse replay received_at line %d: %w", lineNo, err)
		}
		event := new(storagepb.DatasetRowsUpserted)
		if err := protojson.Unmarshal(raw.Event, event); err != nil {
			return nil, fmt.Errorf("decode replay event line %d: %w", lineNo, err)
		}
		out = append(out, trigger.ReplayEvent{MessageID: raw.MessageID, Event: event, ReceivedAt: receivedAt})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read replay input: %w", err)
	}
	return out, nil
}

func runReplay(ctx context.Context, cfg cliConfig, out io.Writer) error {
	if cfg.ReplayInput == "" || cfg.SpaceID == "" || cfg.DatasetID == "" || cfg.FactorVersion == "" || cfg.TargetRunID == "" {
		return errors.New("--input, --space, --dataset, --factor-version and --target-run-id are required")
	}
	req := trigger.ReplayRequest{
		SpaceID: cfg.SpaceID, DatasetID: cfg.DatasetID, StartTime: cfg.ReplayStart, EndTime: cfg.ReplayEnd,
		FactorVersion: cfg.FactorVersion, TargetRunID: cfg.TargetRunID,
	}
	if err := req.Validate(); err != nil {
		return err
	}
	db, err := store.Open(&store.Options{Path: cfg.DBPath})
	if err != nil {
		return err
	}
	if err := db.ApplySchema(factorschema.AllSQL()); err != nil {
		_ = db.Close()
		return fmt.Errorf("apply factor schema: %w", err)
	}
	bindings, err := db.Bindings().ListEnabled(ctx)
	if err != nil {
		_ = db.Close()
		return err
	}
	bindings = replayBindings(bindings, cfg.SpaceID, cfg.DatasetID)
	if len(bindings) == 0 {
		_ = db.Close()
		return fmt.Errorf("no enabled bindings for %s/%s", cfg.SpaceID, cfg.DatasetID)
	}
	factors, err := db.Factors().ListEnabledTimeseries(ctx)
	if err != nil {
		_ = db.Close()
		return err
	}
	factorByID := make(map[string]domain.FactorDef, len(factors))
	factorSourcePaths := make(map[string]string, len(bindings))
	for _, factor := range factors {
		factorByID[factor.FactorID] = factor
	}
	for _, binding := range bindings {
		factor, ok := factorByID[binding.FactorID]
		if !ok {
			_ = db.Close()
			return fmt.Errorf("enabled factor %s for replay binding is missing", binding.FactorID)
		}
		path, err := resolveReplaySourcePath(cfg.FactorsDir, cfg.FactorVersion, factor)
		if err != nil {
			_ = db.Close()
			return err
		}
		factorSourcePaths[factor.FactorID] = path
	}
	if err := db.Close(); err != nil {
		return err
	}
	batcher := trigger.NewEventBatcher(2*time.Second, bindings)
	tasks, err := batcher.ReplayRange(ctx, req, fileReplaySource{path: cfg.ReplayInput})
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return fmt.Errorf("replay produced no tasks for %s/%s in requested range", cfg.SpaceID, cfg.DatasetID)
	}
	for _, task := range tasks {
		runCfg := cfg
		runCfg.SpaceID, runCfg.DatasetID = task.SpaceID, task.SourceDataset
		runCfg.TargetDataset = task.TargetDataset
		runCfg.SubjectID, runCfg.Freq, runCfg.BarTime = task.SubjectID, task.Freq, task.BarTime
		runCfg.FactorIDs = append([]string(nil), task.FactorIDs...)
		runCfg.FactorSourcePaths = factorSourcePaths
		runCfg.TaskID = fmt.Sprintf("replay-%s-%s-%s-%d", cfg.TargetRunID, task.SubjectID, task.Freq, task.BarTime.UTC().UnixNano())
		if err := runOnce(ctx, runCfg, out); err != nil {
			return fmt.Errorf("execute replay task %s: %w", runCfg.TaskID, err)
		}
	}
	return nil
}

func replayBindings(bindings []domain.FactorBinding, spaceID, datasetID string) []domain.FactorBinding {
	out := make([]domain.FactorBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.SpaceID == spaceID && binding.SourceDataset == datasetID {
			out = append(out, binding)
		}
	}
	return out
}

// resolveReplaySourcePath only accepts an immutable registry version. The
// live factors/<name>.py file is intentionally not a replay fallback.
func resolveReplaySourcePath(factorsDir, version string, factor domain.FactorDef) (string, error) {
	if strings.TrimSpace(version) == "" || strings.ContainsAny(version, `/\\`) || version == "." || version == ".." {
		return "", fmt.Errorf("factor version %q is invalid", version)
	}
	path := filepath.Join(factorsDir, ".versions", "factor", factor.Name, version, "module.py")
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("factor version %q for %s is unavailable at %s: %w", version, factor.FactorID, path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("factor version %q for %s is not a regular file: %s", version, factor.FactorID, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read factor version %q for %s: %w", version, factor.FactorID, err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != version {
		return "", fmt.Errorf("factor version %q for %s does not match source hash", version, factor.FactorID)
	}
	return path, nil
}
