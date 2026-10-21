// moox-importer 把历史时序数据文件导入因子机的本地时序存储（设计第 7 节）。
//
// 用法：
//
//	moox-importer run --space crypto --dataset <dataset> --freq 1m --path /data/moox/import/spot_1m/ [--allow-missing] [--recalc]
//	moox-importer status --job-db ./importer.db [--job <任务ID>]
//
// 写入因子机本地存储经过因子引擎的本地接口（签名鉴权，鉴权密钥取自环境变量 MOOX_HEALTH_AUTH_ACCESS_KEY / MOOX_HEALTH_AUTH_SECRET_KEY）；
// 同时写 Storage 用 moox-cli import（它带着 Storage 的网关与鉴权）。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mooyang-code/moox/packages/seriesimport/checkpoint"
	"github.com/mooyang-code/moox/packages/seriesimport/engineclient"
	"github.com/mooyang-code/moox/packages/seriesimport/runner"
	"github.com/mooyang-code/moox/packages/seriesimport/target"
)

var Version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("用法：moox-importer run|status|version")
	}
	switch args[0] {
	case "run":
		return runImport(ctx, args[1:], out)
	case "status":
		return status(args[1:], out)
	case "version":
		_, err := fmt.Fprintf(out, "moox-importer %s\n", Version)
		return err
	default:
		return fmt.Errorf("未知命令 %q（run、status、version）", args[0])
	}
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func runImport(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	space := fs.String("space", "", "Space ID")
	dataset := fs.String("dataset", "", "Dataset ID")
	freq := fs.String("freq", "", "频率，例如 1m")
	var paths listFlag
	fs.Var(&paths, "path", "数据文件或目录（可重复）")
	format := fs.String("format", "csv", "数据格式")
	subject := fs.String("subject", "", "标的 ID；不写时用文件名（去掉扩展名）")
	timeColumn := fs.String("time-column", "data_time", "时间列名")
	seriesTag := fs.String("series-tag", "", "统一指定 series_tag（文件里不能再有 series_tag 列）")
	batchRows := fs.Int("batch-rows", 10000, "每批最多行数（不会拆开同一个时间点）")
	allowMissing := fs.Bool("allow-missing", false, "允许序列内部缺周期（交易所本身缺数据），缺的周期写成带标记的填充行")
	recalc := fs.Bool("recalc", false, "导入完成后对已算出过因子值的区间重算（记入失效重算待办）")
	dryRun := fs.Bool("dry-run", false, "只校验不写入")
	engine := fs.String("engine", "http://127.0.0.1:11417", "因子引擎健康检查地址（本地写入接口挂在它上面）")
	jobDB := fs.String("job-db", "./importer.db", "断点库路径")
	jobID := fs.String("job", "", "任务 ID；不写时按请求与文件特征生成，重跑同一请求自动续传")
	if err := fs.Parse(args); err != nil {
		return err
	}
	step, err := stepOf(*freq)
	if err != nil {
		return err
	}
	client := &engineclient.Client{Endpoint: *engine, AuthVersion: os.Getenv("MOOX_HEALTH_AUTH_VERSION"),
		AccessKey: os.Getenv("MOOX_HEALTH_AUTH_ACCESS_KEY"), SecretKey: os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY")}
	fields, err := client.Fields(ctx, *space, *dataset, *freq)
	if err != nil {
		return fmt.Errorf("读取数据集字段：%w", err)
	}
	meta := runner.Meta{Fields: fields, SubjectInScope: func(ctx context.Context, s string) (bool, error) {
		return client.SubjectInScope(ctx, *space, *dataset, s)
	}}
	var targets []runner.Target
	if !*dryRun {
		targets = []runner.Target{&target.Factor{Endpoint: *engine, AuthVersion: client.AuthVersion, AccessKey: client.AccessKey, SecretKey: client.SecretKey}}
	}
	cps, err := checkpoint.Open(*jobDB)
	if err != nil {
		return err
	}
	defer cps.Close()
	summary, err := runner.Run(ctx, runner.Request{
		Space: *space, Dataset: *dataset, Freq: *freq, Paths: paths, Format: *format, Subject: *subject,
		TimeColumn: *timeColumn, SeriesTag: *seriesTag, BatchRows: *batchRows, AllowMissing: *allowMissing, Recalc: *recalc,
		DryRun: *dryRun, JobID: *jobID, Step: step,
	}, meta, targets, cps)
	if encErr := json.NewEncoder(out).Encode(summary); encErr != nil && err == nil {
		err = encErr
	}
	return err
}

func status(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	jobDB := fs.String("job-db", "./importer.db", "断点库路径")
	job := fs.String("job", "", "任务 ID；不写时列出全部任务")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cps, err := checkpoint.Open(*jobDB)
	if err != nil {
		return err
	}
	defer cps.Close()
	if *job == "" {
		jobs, err := cps.Jobs()
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"jobs": jobs})
	}
	entries, err := cps.List(*job)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"job": *job, "entries": entries})
}

func stepOf(freq string) (time.Duration, error) {
	steps := map[string]time.Duration{"1m": time.Minute, "5m": 5 * time.Minute, "15m": 15 * time.Minute, "30m": 30 * time.Minute,
		"1h": time.Hour, "4h": 4 * time.Hour, "1d": 24 * time.Hour}
	if d, ok := steps[freq]; ok {
		return d, nil
	}
	return 0, fmt.Errorf("不支持的频率 %q（支持 1m 5m 15m 30m 1h 4h 1d）", freq)
}
