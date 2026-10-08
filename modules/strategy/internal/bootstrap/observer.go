package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	moduleStage       = "evaluate"
	moduleHealthCheck = "strategy-targets"
)

// failureReasons 是计入模块健康检查失败的跳过原因；其余跳过是业务上的"本期不能求值"，只通过实例的数据集新鲜度告警。
var failureReasons = map[string]bool{
	trigger.SkipInfraRetryExhausted: true,
	input.SkipConfigError:           true,
	input.SkipAmbiguousSeries:       true,
}

var metricLabelPattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// instanceObserver 把每个启用实例登记为 Monitor 的期望数据集（间隔 = bar 时长），每期上报一次运行；
// 同时累计 moox_strategy_period_total{instance_id,result,reason}。
type instanceObserver struct {
	store    *store.Store
	datasets *report.DatasetMetrics
	module   *report.ModuleMetrics
	periods  *prometheus.CounterVec
	now      func() time.Time
	logf     func(format string, args ...any)

	mu       sync.Mutex
	expected map[string]report.DatasetExpectation
}

func newInstanceObserver(repo *store.Store, registerer prometheus.Registerer, logf func(string, ...any)) (*instanceObserver, error) {
	datasets, err := report.NewDatasetMetrics(registerer, "strategy")
	if err != nil {
		return nil, fmt.Errorf("创建策略数据集指标失败：%w", err)
	}
	module, err := report.NewModuleMetrics(registerer, "strategy", report.HealthCheckIDsForModule("strategy"))
	if err != nil {
		return nil, fmt.Errorf("创建策略模块指标失败：%w", err)
	}
	periods := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_strategy_period_total", Help: "策略实例处理的周期数，按结果与跳过原因分类。"}, []string{"instance_id", "result", "reason"})
	if err := registerer.Register(periods); err != nil {
		return nil, fmt.Errorf("注册策略周期指标失败：%w", err)
	}
	return &instanceObserver{store: repo, datasets: datasets, module: module, periods: periods, now: time.Now, logf: logf, expected: map[string]report.DatasetExpectation{}}, nil
}

// datasetKey 由实例派生 Monitor 的数据集标识；不合法的空间或周期返回 false。
func datasetKey(instance store.Instance, bar string) (report.DatasetExpectation, bool) {
	if !metricLabelPattern.MatchString(instance.SpaceID) {
		return report.DatasetExpectation{}, false
	}
	freq, err := frequency.Parse(bar)
	if err != nil || freq.NominalDuration() <= 0 {
		return report.DatasetExpectation{}, false
	}
	id := strings.ToLower(instance.InstanceID)
	datasetID := "strategy_" + id
	if !metricLabelPattern.MatchString(datasetID) {
		sum := sha256.Sum256([]byte(instance.InstanceID))
		datasetID = "strategy_" + hex.EncodeToString(sum[:8])
	}
	return report.DatasetExpectation{Key: report.DatasetKey{SpaceID: instance.SpaceID, DatasetID: datasetID, Freq: string(freq)}, Interval: freq.NominalDuration()}, true
}

// refresh 用当前启用实例重建期望数据集清单；停用的实例随之移出。
func (o *instanceObserver) refresh(ctx context.Context) error {
	enabled := true
	instances, err := o.store.ListInstances(ctx, "", &enabled)
	if err != nil {
		o.datasets.ObserveInventoryRefreshError()
		return fmt.Errorf("读取启用实例：%w", err)
	}
	next := make(map[string]report.DatasetExpectation, len(instances))
	for _, instance := range instances {
		resolved, err := input.ParseResolved(instance.ResolvedJSON)
		if err != nil {
			continue
		}
		if expectation, ok := datasetKey(instance, resolved.Bar); ok {
			next[instance.InstanceID] = expectation
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.datasets.ReplaceExpected(expectations(next)); err != nil {
		return err
	}
	o.expected = next
	return nil
}

func expectations(values map[string]report.DatasetExpectation) []report.DatasetExpectation {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]report.DatasetExpectation, 0, len(ids))
	for _, id := range ids {
		out = append(out, values[id])
	}
	return out
}

// ObservePeriod 记录一个处理周期：ok → success（推进输出水位），skipped → incomplete。
func (o *instanceObserver) ObservePeriod(instance store.Instance, bar string, barEnd time.Time, result, reason string) {
	o.periods.WithLabelValues(instance.InstanceID, result, reason).Inc()
	o.mu.Lock()
	defer o.mu.Unlock()
	expectation, ok := o.expected[instance.InstanceID]
	if !ok {
		if expectation, ok = datasetKey(instance, bar); !ok {
			return
		}
		next := make(map[string]report.DatasetExpectation, len(o.expected)+1)
		for id, value := range o.expected {
			next[id] = value
		}
		next[instance.InstanceID] = expectation
		if err := o.datasets.ReplaceExpected(expectations(next)); err != nil {
			o.log("登记实例 %s 的期望数据集失败：%v", instance.InstanceID, err)
			return
		}
		o.expected = next
	}
	finishedAt := o.now().UTC()
	observation := report.DatasetObservation{Key: expectation.Key, Result: "incomplete", FinishedAt: finishedAt, InputWatermark: barEnd}
	if result == store.StatusOK {
		observation.Result = "success"
		observation.OutputWatermark = barEnd
	}
	if err := o.datasets.ObserveRun(observation); err != nil {
		o.log("上报实例 %s 的周期运行失败：%v", instance.InstanceID, err)
	}
	switch {
	case result == store.StatusOK:
		_ = o.module.ObserveRun(moduleStage, "success", moduleHealthCheck, finishedAt)
	case failureReasons[reason]:
		_ = o.module.ObserveRun(moduleStage, "error", moduleHealthCheck, finishedAt)
	case reason == readiness.ReasonFactorChanged:
		// 因子定义变化需要人工重新启用，计入模块失败以便提醒。
		_ = o.module.ObserveRun(moduleStage, "error", moduleHealthCheck, finishedAt)
	}
}

func (o *instanceObserver) log(format string, args ...any) {
	if o.logf != nil {
		o.logf(format, args...)
	}
}
