package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/merge"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	trpc "trpc.group/trpc-go/trpc-go"
)

const (
	mergeAppID         = "moox-merge"
	mergeHealthService = "trpc.moox.factor.merge.Health"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (err error) {
	app := flag.String("config", "config/merge-app.yaml", "merge application config")
	framework := flag.String("conf", "config/merge-trpc.yaml", "merge tRPC config")
	flag.Parse()
	cfg, err := merge.LoadProcessConfig(*app)
	if err != nil {
		return err
	}
	if err := gatewayauth.ValidateTargetNode(cfg.Storage.GatewayNodeID); err != nil {
		return err
	}
	if strings.TrimSpace(os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")) == "" {
		return errors.New("merge storage primary secret is required")
	}
	trpc.ServerConfigPath = *framework
	s := trpc.NewServer()
	trpclog.InstallServiceName("factor-merge")
	if s.Service(mergeHealthService) == nil {
		return errors.New("merge health service is required")
	}
	ctx, cancel := context.WithCancel(trpc.BackgroundContext())
	defer cancel()
	ledger, err := merge.Open(merge.Options{Path: cfg.Database.Path})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, ledger.Close()) }()
	credentials, err := gatewayauth.ResolveCredentials(cfg.Storage.KeyID, cfg.Storage.HMACKeyFile)
	if err != nil {
		return fmt.Errorf("load merge storage credentials: %w", err)
	}
	target := storageio.NormalizeStorageTarget(cfg.Storage.GatewayTarget, "11003")
	primary := storagepb.NewPrimaryStoreClientProxy(gatewayauth.NewTRPCClientOptions(target, cfg.Storage.GatewayNodeID, credentials)...)
	assemblers := make([]*merge.Assembler, 0, len(cfg.Definitions))
	for _, def := range cfg.Definitions {
		if def.MergeMode != domain.MergeModeSystem {
			continue
		}
		committer := merge.NewStorageCommitter(primary, mergeAuthInfo(), def.SpaceID, targetFields(def))
		assembler, assemblerErr := merge.NewAssembler(ledger, def, committer)
		if assemblerErr != nil {
			return assemblerErr
		}
		assemblers = append(assemblers, assembler)
	}
	consumer, err := merge.StartRowConsumer(ctx, cfg, merge.NewRowHandler(assemblers...))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, consumer.Close()) }()
	state := health.New("factor-merge", cfg.MergeID, "", "")
	state.SetReady(true)
	if err := health.Register(s.Service(mergeHealthService), state); err != nil {
		return err
	}
	s.RegisterOnShutdown(func() {
		state.SetReady(false)
		cancel()
	})
	return s.Serve()
}

func targetFields(def domain.MergedDataset) []string {
	fields := make([]string, 0, len(def.FieldMappings))
	for _, mapping := range def.FieldMappings {
		fields = append(fields, mapping.TargetField)
	}
	return fields
}

func mergeAuthInfo() *commonpb.AuthInfo {
	auth := &commonpb.AuthInfo{AppId: mergeAppID, Operator: mergeAppID, RequestId: fmt.Sprintf("factor-merge-%d", time.Now().UnixNano())}
	if secret := os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET"); strings.TrimSpace(secret) != "" {
		auth.AppKey = mooxsecurity.HMACSHA256Hex(secret, []byte(auth.AppId))
	}
	return auth
}
