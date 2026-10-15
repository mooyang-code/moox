package klinecheck

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

// 健康等级。
const (
	HealthOK    = "ok"
	HealthWarn  = "warn"
	HealthError = "error"
)

const (
	// lagWarn、lagError 是自动检查滞后的告警阈值：正常情况下滞后不超过一个扫描槽（5 分钟）加偏移。
	lagWarn  = 15 * time.Minute
	lagError = time.Hour
	// healthWindow 是统计实时缺口与失败分块的时间窗口。
	healthWindow = 24 * time.Hour
	// maxHealthChunks 是统计实时缺口时最多读取的自动补采分块数。
	maxHealthChunks = 50000
)

// HealthStore 是计算数据集健康度需要的只读查询，由 *store.BackfillRepository 实现。
type HealthStore interface {
	Watermarks(context.Context, string) (map[string]time.Time, error)
	AutoChunksSince(context.Context, string, time.Time, int) ([]store.BackfillChunk, error)
	FailedChunkCounts(context.Context, string, time.Time) (map[string]int, error)
	AbsentBars(context.Context, string) (map[string]int, error)
	HistoryScans(context.Context, string) (map[string]store.HistoryScan, error)
}

// DatasetHealth 是一个正在采集的数据集（某个频率）的健康度。
type DatasetHealth struct {
	DatasetID string
	Frequency string
	Subjects  int
	// EffectiveFrom 是最早一次采集计划生效的周期；ScannedUntil 是自动检查的水位，没有扫描过为零值。
	EffectiveFrom time.Time
	ScannedUntil  time.Time
	// Lag 是自动检查落后于“当前应扫到的时间”的时长。
	Lag time.Duration
	// GapBars、GapSubjects 是窗口内自动补采发现的实时缺口（实时采集漏掉的 K 线）。
	GapBars      int
	GapSubjects  int
	FailedChunks int
	History      *store.HistoryScan
	AbsentBars   int
	Level        string
	Reasons      []string
}

// Health 计算空间里每个正在采集的数据集的健康度，异常排前。
func Health(ctx context.Context, plans PlanHistory, repo HealthStore, spaceID string, now time.Time) ([]DatasetHealth, error) {
	records, err := plans.ListAll(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	timeline, err := BuildTimeline(records)
	if err != nil {
		return nil, err
	}
	watermarks, err := repo.Watermarks(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	since := now.Add(-healthWindow)
	chunks, err := repo.AutoChunksSince(ctx, spaceID, since, maxHealthChunks)
	if err != nil {
		return nil, err
	}
	failed, err := repo.FailedChunkCounts(ctx, spaceID, since)
	if err != nil {
		return nil, err
	}
	absent, err := repo.AbsentBars(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	scans, err := repo.HistoryScans(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	type gapStat struct {
		bars     int
		subjects map[string]bool
	}
	gaps := map[string]*gapStat{}
	for _, chunk := range chunks {
		frequency, err := marketdata.ParseFrequency(chunk.Frequency)
		if err != nil {
			continue
		}
		key := DatasetKey(chunk.DatasetID, chunk.Frequency)
		stat := gaps[key]
		if stat == nil {
			stat = &gapStat{subjects: map[string]bool{}}
			gaps[key] = stat
		}
		stat.bars += barCount(frequency, chunk.From, chunk.To)
		stat.subjects[chunk.SubjectID] = true
	}

	var out []DatasetHealth
	for key, segments := range timeline {
		if len(segments) == 0 || segments[len(segments)-1].To != 0 {
			continue // 数据集已停止采集
		}
		datasetID, frequencyName, _ := strings.Cut(key, "|")
		frequency, err := marketdata.ParseFrequency(frequencyName)
		if err != nil {
			continue
		}
		last := segments[len(segments)-1]
		health := DatasetHealth{
			DatasetID: datasetID, Frequency: frequencyName, Subjects: len(last.Subjects), EffectiveFrom: time.Unix(segments[0].From, 0).UTC(),
			ScannedUntil: watermarks[key], FailedChunks: failed[key], AbsentBars: absent[key],
		}
		// 与自动补采一致：当前应扫到“已过迟到容忍的最后一个完整周期”。
		due := alignDown(frequency, now.Add(-time.Duration(last.LatenessS)*time.Second-frequency.NominalDuration()))
		baseline := health.ScannedUntil
		if baseline.IsZero() {
			baseline = health.EffectiveFrom
		}
		health.Lag = max(due.Sub(baseline), 0)
		if stat := gaps[key]; stat != nil {
			health.GapBars, health.GapSubjects = stat.bars, len(stat.subjects)
		}
		if scan, ok := scans[key]; ok {
			health.History = &scan
		}
		health.Level, health.Reasons = assess(health)
		out = append(out, health)
	}
	sort.Slice(out, func(i, j int) bool {
		if rank(out[i].Level) != rank(out[j].Level) {
			return rank(out[i].Level) > rank(out[j].Level)
		}
		return DatasetKey(out[i].DatasetID, out[i].Frequency) < DatasetKey(out[j].DatasetID, out[j].Frequency)
	})
	return out, nil
}

// assess 按阈值给出健康等级与原因。实时缺口只要被自动补采覆盖就不影响等级。
func assess(health DatasetHealth) (string, []string) {
	level := HealthOK
	var reasons []string
	raise := func(to, reason string) {
		if rank(to) > rank(level) {
			level = to
		}
		reasons = append(reasons, reason)
	}
	switch {
	case health.Lag > lagError:
		raise(HealthError, "自动检查滞后 "+FormatDuration(health.Lag))
	case health.Lag > lagWarn:
		raise(HealthWarn, "自动检查滞后 "+FormatDuration(health.Lag))
	}
	if health.FailedChunks > 0 {
		raise(HealthWarn, fmt.Sprintf("24 小时内 %d 个补采分块失败", health.FailedChunks))
	}
	if history := health.History; history != nil {
		if history.LastError != "" {
			raise(HealthWarn, "历史缺口扫描失败："+history.LastError)
		} else if history.GapBars > 0 {
			raise(HealthWarn, fmt.Sprintf("历史缺口 %d 根（%d 个标的）待补采确认", history.GapBars, history.GapSubjects))
		}
	}
	return level, reasons
}

func rank(level string) int {
	switch level {
	case HealthError:
		return 2
	case HealthWarn:
		return 1
	}
	return 0
}

// WorseLevel 返回两个等级中更严重的一个。
func WorseLevel(a, b string) string {
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// FormatDuration 把时长写成“N 分钟”“N 小时 M 分钟”“N 天”这样的中文。
func FormatDuration(d time.Duration) string {
	minutes := int(d.Round(time.Minute) / time.Minute)
	switch {
	case minutes < 60:
		return fmt.Sprintf("%d 分钟", minutes)
	case minutes < 24*60:
		if minutes%60 == 0 {
			return fmt.Sprintf("%d 小时", minutes/60)
		}
		return fmt.Sprintf("%d 小时 %d 分钟", minutes/60, minutes%60)
	}
	return fmt.Sprintf("%d 天", minutes/(24*60))
}
