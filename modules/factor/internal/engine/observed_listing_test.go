package engine

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/memadmit"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/stretchr/testify/require"
)

// listingSource 是可以改的数据集在册清单（下架、上架），同时提供源数据集的列。
type listingSource struct {
	mu       sync.Mutex
	subjects []string
	calls    int
}

func (l *listingSource) set(subjects ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.subjects = append([]string(nil), subjects...)
}

func (l *listingSource) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	return append([]string(nil), l.subjects...), nil
}

func (*listingSource) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close", "open"}, nil
}

// listingScenario 是只有 obs 一个任务的数据源：A、B、C（基数 100、300、500）一直在册，GONE（基数 200）在第 9 分钟前下架。
type listingScenario struct {
	t       *testing.T
	storage *sharedStorage
	listing *listingSource
	clock   time.Time
	synced  *syncedScope
	data    *localdata.Data
	runner  *pipeline.Runner
	obs     domain.EngineTask
}

func newListingScenario(t *testing.T) *listingScenario {
	t.Helper()
	sc := &listingScenario{t: t, storage: newSharedStorage(), listing: &listingSource{}, clock: rt0.Add(9 * time.Minute)}
	for subject, base := range map[string]float64{"A": 100, "B": 300, "C": 500} {
		sc.storage.put(subject, base, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)
	}
	sc.storage.put("GONE", 200, 0, 1, 2, 3, 4, 5, 6, 7, 8)
	sc.listing.set("A", "B", "C", "GONE")
	sc.obs, _ = sharedTasks()
	sc.synced = &syncedScope{catalog: func() []domain.EngineTask { return []domain.EngineTask{sc.obs} }, source: sc.listing,
		now: func() time.Time { return sc.clock }}
	store, err := seriesstore.Open(seriesstore.Options{Dir: t.TempDir(), BlockCacheBytes: 8 << 20, DisableSync: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	sc.data = localdata.New(localdata.Config{Store: store, Storage: sc.storage, Admit: memadmit.New(0), Workers: 1, BatchSubjects: 2,
		Now: func() time.Time { return sc.clock }, Synced: sc.synced.Subjects})
	sc.runner = pipeline.NewRunner(sc.storage, sc.data, periodclock.Continuous{}, pipeline.Config{ComputeWorkers: 1})
	return sc
}

func (sc *listingScenario) live(minute int, universe ...string) float64 {
	sc.t.Helper()
	plan, err := pipeline.BuildLivePlan(periodclock.Continuous{}, pipeline.LiveInput{Set: sc.obs.Set, Members: sc.obs.Members,
		PeriodTime: rt0.Add(time.Duration(minute) * time.Minute), Universe: universe, CarryColumns: []string{"close"}})
	require.NoError(sc.t, err)
	_, err = sc.runner.Run(context.Background(), plan)
	require.NoError(sc.t, err)
	sc.data.Drain()
	return sc.storage.value(sc.t, "result_ftask_obs", "A", minute, "obs")
}

func (sc *listingScenario) recalc(minute int, universe ...string) float64 {
	sc.t.Helper()
	plan := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: sc.obs.Set, Members: sc.obs.Members,
		TargetStart: rt0.Add(time.Duration(minute) * time.Minute), TargetEnd: rt0.Add(time.Duration(minute+1) * time.Minute),
		Expected: universe, Available: universe, CarryColumns: []string{"close"}}
	_, err := sc.runner.Run(context.Background(), plan)
	require.NoError(sc.t, err)
	return sc.storage.value(sc.t, "result_ftask_obs", "A", minute, "obs")
}

// 刚下架时“正在同步”的缓存还是旧清单：重新 Prepare 不再按它把 GONE 移出窗口，缓存过期后实时与补算逐位相同。
func TestObservedStaleSyncedCacheDoesNotDropWindow(t *testing.T) {
	sc := newListingScenario(t)
	ctx := context.Background()
	require.NoError(t, sc.data.Prepare(ctx, sc.obs, []string{"A", "B", "C", "GONE"}))
	_, err := sc.synced.Subjects(ctx, seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m"}) // 缓存旧清单
	require.NoError(t, err)
	// GONE 下架：数据集清单变了，缓存还没过期，任务按新全集重新准备。
	sc.listing.set("A", "B", "C")
	sc.clock = rt0.Add(10 * time.Minute)
	require.NoError(t, sc.data.Prepare(ctx, sc.obs, []string{"A", "B", "C"}))
	// 缓存过期之后的实时与补算：第 8 分钟的截面有 GONE。
	sc.clock = rt0.Add(11*time.Minute + syncedScopeTTL)
	live := sc.live(10, "A", "B", "C")
	recalc := sc.recalc(10, "A", "B", "C")
	require.InDelta(t, (0.25+1.0/3+1.0/3)/3, live, 1e-12)
	require.Equal(t, math.Float64bits(live), math.Float64bits(recalc))
}

// 预载取到新清单时直接更新“正在同步”的缓存（preloader.onSubjects）：下架、上架之后马上算出的实时值，
// 与缓存过期之后的补算逐位相同，不必等一分钟。
func TestObservedPreloadUpdatesSyncedScope(t *testing.T) {
	for _, change := range []string{"delist", "list"} {
		t.Run(change, func(t *testing.T) {
			sc := newListingScenario(t)
			ctx := context.Background()
			preload := newPreloader(nil, sc.listing, sc.data, nil, nil)
			preload.onSubjects = sc.synced.Update
			if change == "list" {
				// 上架：NEW 原来不在册（本地有它的数据），第 10 分钟起在册。
				sc.storage.put("NEW", 400, 6, 7, 8, 9, 10, 11)
				sc.listing.set("A", "B", "C")
			}
			subjects, err := preload.prepare(ctx, sc.obs)
			require.NoError(t, err)
			preload.markPrepared(sc.obs, subjects)
			// 上一期的计算把旧清单放进了缓存（没过期）。
			sc.clock = rt0.Add(9*time.Minute + 30*time.Second)
			_, err = sc.synced.Subjects(ctx, seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m"})
			require.NoError(t, err)
			universe := []string{"A", "B", "C"}
			if change == "delist" {
				sc.listing.set("A", "B", "C")
			} else {
				sc.listing.set("A", "B", "C", "NEW")
				universe = []string{"A", "B", "C", "NEW"}
			}
			sc.clock = rt0.Add(10 * time.Minute)
			require.True(t, preload.subjectsChanged(ctx, sc.obs), "预载发现清单变化")
			_, err = preload.prepare(ctx, sc.obs)
			require.NoError(t, err)
			sc.clock = rt0.Add(10*time.Minute + 20*time.Second) // 旧清单的缓存还没过期
			live := sc.live(10, universe...)
			sc.clock = rt0.Add(11*time.Minute + syncedScopeTTL)
			recalc := sc.recalc(10, universe...)
			require.Equal(t, math.Float64bits(recalc), math.Float64bits(live), "下架、上架后马上算出的实时值与补算相同")
		})
	}
}
