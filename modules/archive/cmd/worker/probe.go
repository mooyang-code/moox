package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/worker/config"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/source"
	"github.com/mooyang-code/moox/packages/gatewayauth"
)

// probe 用 worker 自己的身份读几页区间数据并打印每页耗时，用来排查读取为什么慢或超时（不写任何东西）。
func probe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	configPath := fs.String("config", "config/app.yaml", "archive worker config")
	space := fs.String("space", "", "空间 ID")
	dataset := fs.String("dataset", "", "数据集 ID")
	span := fs.Duration("span", 76*time.Minute, "读取区间长度（结束于现在）")
	pageRows := fs.Int("page-rows", 2000, "每页行数")
	pages := fs.Int("pages", 5, "最多读几页")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *space == "" || *dataset == "" {
		return fmt.Errorf("--space 与 --dataset 必填")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	gateway, err := cfg.OpenGateway()
	if err != nil {
		return err
	}
	defer gateway.Close()
	secret, err := gatewayauth.ReadSigningSecret(cfg.StorageSecretPath())
	if err != nil {
		return err
	}
	client := source.New(gateway, secret)
	ctx := context.Background()
	started := time.Now()
	columns, err := client.Columns(ctx, *space, *dataset)
	if err != nil {
		return err
	}
	fmt.Printf("columns=%d in %s\n", len(columns), time.Since(started).Round(time.Millisecond))
	end := time.Now().UTC().Truncate(time.Minute)
	request := source.Range{SpaceID: *space, DatasetID: *dataset, Columns: columns, Start: end.Add(-*span), End: end, PageRows: *pageRows}
	for i := 1; i <= *pages; i++ {
		at := time.Now()
		page, err := client.ReadRange(ctx, request)
		if err != nil {
			return fmt.Errorf("page %d after %s: %w", i, time.Since(at).Round(time.Millisecond), err)
		}
		fmt.Printf("page %d: rows=%d bytes=%d took=%s more=%t\n", i, len(page.Rows), page.Bytes, time.Since(at).Round(time.Millisecond), page.NextCursor != "")
		if page.NextCursor == "" {
			return nil
		}
		request.Cursor = page.NextCursor
	}
	return nil
}
