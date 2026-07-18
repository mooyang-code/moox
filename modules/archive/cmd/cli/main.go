package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/archive/internal/backfill"
	"github.com/mooyang-code/moox/modules/archive/internal/config"
	"github.com/mooyang-code/moox/modules/archive/internal/cosstore"
	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/modules/archive/internal/parquetio"
	"github.com/mooyang-code/moox/modules/archive/internal/writer"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	trpc "trpc.group/trpc-go/trpc-go"
)

type cliConfig struct {
	Command    string
	ConfigPath string
	Space      string
	Dataset    string
	Subject    string
	Freq       string
	Start      string
	End        string
	Confirm    bool
	Month      string
}

func main() {
	if err := run(trpc.BackgroundContext(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "{\"ok\":false,\"error\":%q}\n", err)
		os.Exit(1)
	}
}
func parseArgs(args []string) (cliConfig, error) {
	if len(args) == 0 {
		return cliConfig{}, errors.New("command is required")
	}
	cfg := cliConfig{Command: args[0], ConfigPath: "config/app.yaml"}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "archive config")
	fs.StringVar(&cfg.Space, "space", "", "space id")
	fs.StringVar(&cfg.Dataset, "dataset", "", "dataset id")
	fs.StringVar(&cfg.Subject, "subject", "", "subject id")
	fs.StringVar(&cfg.Freq, "freq", "", "frequency")
	fs.StringVar(&cfg.Start, "start", "", "start RFC3339")
	fs.StringVar(&cfg.End, "end", "", "end RFC3339")
	fs.BoolVar(&cfg.Confirm, "confirm", false, "confirm backfill")
	fs.StringVar(&cfg.Month, "month", "", "month YYYYMM")
	if err := fs.Parse(args[1:]); err != nil {
		return cliConfig{}, err
	}
	switch cfg.Command {
	case "backfill", "compact", "verify", "sync-cos", "status":
		return cfg, nil
	default:
		return cliConfig{}, fmt.Errorf("unknown command %q", cfg.Command)
	}
}
func run(ctx context.Context, args []string, out io.Writer) error {
	cfg, err := parseArgs(args)
	if err != nil {
		return err
	}
	appCfg, err := config.Load(cfg.ConfigPath)
	if err != nil {
		return err
	}
	switch cfg.Command {
	case "backfill":
		return runBackfill(ctx, cfg, appCfg, out)
	case "compact":
		return runCompact(ctx, cfg, appCfg, out)
	case "verify":
		return runVerify(ctx, cfg, appCfg, out)
	case "sync-cos":
		return runSyncCOS(ctx, appCfg, out)
	case "status":
		return runStatus(ctx, appCfg, out)
	}
	return nil
}

func runSyncCOS(ctx context.Context, cfg *config.Config, out io.Writer) error {
	if !cfg.Archive.COS.Enabled {
		return errors.New("COS sync is disabled")
	}
	client, err := cosstore.New(cfg.Archive.COS.Region, cfg.Archive.COS.Bucket, cfg.Archive.RootDir, cfg.Archive.COS.Prefix, "", "")
	if err != nil {
		return err
	}
	if err := (cosstore.Syncer{Client: client, Root: cfg.Archive.RootDir, Prefix: cfg.Archive.COS.Prefix, Workers: cfg.Archive.COS.Workers, SyncOpenPartitions: cfg.Archive.COS.SyncOpenPartitions}).Sync(ctx); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"ok": true})
}
func openLocal(cfg *config.Config) (*journal.Store, *writer.Writer, error) {
	store, err := journal.Open(cfg.Archive.StateDir)
	if err != nil {
		return nil, nil, err
	}
	return store, writer.New(store, cfg.Archive.RootDir, cfg.Archive.Materialize.RowGroupRows), nil
}
func runBackfill(ctx context.Context, cli cliConfig, cfg *config.Config, out io.Writer) error {
	if cli.Space == "" || cli.Dataset == "" || cli.Subject == "" || cli.Freq == "" || cli.Start == "" || cli.End == "" {
		return errors.New("backfill requires space, dataset, subject, freq, start and end")
	}
	plan := backfill.Plan{SpaceID: cli.Space, DatasetID: cli.Dataset, SubjectID: cli.Subject, Freq: cli.Freq, Start: cli.Start, End: cli.End, Confirm: cli.Confirm}
	if !cli.Confirm {
		return json.NewEncoder(out).Encode(map[string]any{"ok": true, "confirm_required": true, "partitions": plan.Partitions()})
	}
	store, w, err := openLocal(cfg)
	if err != nil {
		return err
	}
	defer store.Close()
	target := gatewayauth.ServiceGatewayTarget(cfg.Archive.StorageRPC.GatewayTarget)
	credentials, err := gatewayauth.ResolveCredentials(cfg.Archive.StorageRPC.KeyID, cfg.Archive.StorageRPC.HMACKeyFile)
	if err != nil {
		return err
	}
	options := gatewayauth.NewTRPCClientOptions(backfill.NormalizeTarget(target, "11003"), cfg.Archive.StorageRPC.GatewayNodeID, credentials)
	access := storagepb.NewPrimaryStoreClientProxy(options...)
	metadata := storagepb.NewMetadataClientProxy(options...)
	count, err := backfill.New(access, metadata, store, w).Run(ctx, plan)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"ok": true, "rows": count})
}
func runCompact(ctx context.Context, cli cliConfig, cfg *config.Config, out io.Writer) error {
	store, w, err := openLocal(cfg)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := w.WriteDirty(ctx, 100000); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"ok": true})
}
func runStatus(ctx context.Context, cfg *config.Config, out io.Writer) error {
	store, _, err := openLocal(cfg)
	if err != nil {
		return err
	}
	defer store.Close()
	status, err := store.Status(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"ok": true, "pending_rows": status.PendingRows, "dirty_partitions": status.DirtyPartitions})
}
func runVerify(ctx context.Context, cli cliConfig, cfg *config.Config, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var checked int
	err := filepath.WalkDir(cfg.Archive.RootDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".parquet") {
			return nil
		}
		key, err := domain.ParseFileName(entry.Name())
		if err != nil {
			return err
		}
		if cli.Space != "" && cli.Space != key.SpaceID || cli.Dataset != "" && cli.Dataset != key.DatasetID || cli.Subject != "" && cli.Subject != key.SubjectID || cli.Freq != "" && cli.Freq != key.Freq || cli.Month != "" && cli.Month != key.Month {
			return nil
		}
		_, _, _, err = parquetio.Read(path)
		if err != nil {
			return err
		}
		checked++
		return nil
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"ok": true, "files": checked})
}
