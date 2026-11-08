package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// productionDatasets 是线上 8 个归档数据集在 Storage 里的属性（2026-11-08 用 metadata dataset show 读取，只保留目录名用到的字段）。
func productionDatasets() map[string]Dataset {
	active := func(space, id, source, freq string, tags []string, attrs map[string]string) Dataset {
		return Dataset{SpaceID: space, DatasetID: id, DataSourceID: source, Freq: freq, SubjectTags: tags, Attributes: attrs,
			DataKind: "time_series", Status: "active", DataNodeID: "node-0", Retention: "840h"}
	}
	list := []Dataset{
		active("crypto", "dataset_dasftksvjhj2jom4vhd0", "binance", "1m", []string{"binance_spot"},
			map[string]string{"dataset_role": "raw_collection", "market_type": "spot", "owner_module": "collector"}),
		active("crypto", "dataset_dasftksvjhj2jom4vhdg", "binance", "1m", []string{"binance_swap"},
			map[string]string{"dataset_role": "raw_collection", "market_type": "swap", "owner_module": "collector"}),
		active("crypto", "dataset_factor_spot_1m", "binance", "1m", []string{"binance_spot"},
			map[string]string{"dataset_role": "factor_result", "owner_module": "factor", "source_dataset_id": "dataset_dasftksvjhj2jom4vhd0"}),
		active("crypto", "dataset_factor_swap_1m", "binance", "1m", []string{"binance_swap"},
			map[string]string{"dataset_role": "factor_result", "owner_module": "factor", "source_dataset_id": "dataset_dasftksvjhj2jom4vhdg"}),
		active("stockcn", "dataset_dasftksvjhj2jom4vhh0", "eastmoney", "5m", []string{"cn_hs300"},
			map[string]string{"dataset_role": "raw_collection", "market_type": "equity", "owner_module": "collector"}),
		active("stockcn", "dataset_factor_cn_5m", "eastmoney", "5m", []string{"cn_hs300"},
			map[string]string{"dataset_role": "factor_result", "owner_module": "factor", "source_dataset_id": "dataset_dasftksvjhj2jom4vhh0"}),
		active("stockcn", "dataset_stockcn_equity_kline_1m", "stockcn", "1m", nil,
			map[string]string{"dataset_role": "raw_collection", "market_type": "equity", "owner_module": "collector"}),
		active("stockcn", "dataset_stockcn_bond_kline_1m", "market_data", "1m", nil,
			map[string]string{"dataset_role": "raw_collection", "instrument_type": "convertible_bond", "market_id": "stockcn", "owner_module": "collector"}),
	}
	out := map[string]Dataset{}
	for _, ds := range list {
		out[ds.SpaceID+"/"+ds.DatasetID] = ds
	}
	return out
}

// 线上 8 个数据集的目录与计划第 9 节的表格一致。
func TestPrepareSuggestsTheReadableDirectoriesOfTheProductionDatasets(t *testing.T) {
	h := newHarness(t)
	h.datasets.datasets = productionDatasets()
	for _, tc := range []struct{ space, dataset, want string }{
		{"crypto", "dataset_dasftksvjhj2jom4vhd0", "crypto/collect/binance-spot-kline-1m"},
		{"crypto", "dataset_dasftksvjhj2jom4vhdg", "crypto/collect/binance-swap-kline-1m"},
		{"crypto", "dataset_factor_spot_1m", "crypto/factor/binance-spot-1m"},
		{"crypto", "dataset_factor_swap_1m", "crypto/factor/binance-swap-1m"},
		{"stockcn", "dataset_dasftksvjhj2jom4vhh0", "stockcn/collect/hs300-kline-5m"},
		{"stockcn", "dataset_factor_cn_5m", "stockcn/factor/hs300-5m"},
		{"stockcn", "dataset_stockcn_equity_kline_1m", "stockcn/collect/equity-kline-1m"},
		{"stockcn", "dataset_stockcn_bond_kline_1m", "stockcn/collect/bond-kline-1m"},
	} {
		rsp, err := h.mgr.PrepareArchiveTask(context.Background(), &archivepb.PrepareArchiveTaskReq{SpaceId: tc.space, DatasetId: tc.dataset})
		if err != nil {
			t.Fatal(err)
		}
		requireOK(t, rsp.GetRetInfo())
		if got := tc.space + "/" + rsp.GetPurpose() + "/" + rsp.GetSuggestedDirName(); got != tc.want || rsp.GetDirNameConflict() {
			t.Errorf("%s：目录 = %s（冲突 %v），want %s", tc.dataset, got, rsp.GetDirNameConflict(), tc.want)
		}
	}
	// 按建议值建好 8 个任务，彼此不冲突；任务带上用途与目录名，再 Prepare 自己不算冲突。
	for key, ds := range h.datasets.datasets {
		created, _ := h.mgr.CreateArchiveTask(context.Background(), &archivepb.CreateArchiveTaskReq{SpaceId: ds.SpaceID, DatasetId: ds.DatasetID})
		requireOK(t, created.GetRetInfo())
		if created.GetTask().GetPurpose() == "" || created.GetTask().GetDirName() == "" {
			t.Fatalf("%s: task = %v", key, created.GetTask())
		}
		again, _ := h.mgr.PrepareArchiveTask(context.Background(), &archivepb.PrepareArchiveTaskReq{SpaceId: ds.SpaceID, DatasetId: ds.DatasetID})
		if !again.GetExists() || again.GetDirNameConflict() {
			t.Fatalf("%s: 已有任务的数据集占用的是自己的目录名: %v", key, again)
		}
	}
}

func TestSuggestDirNameRules(t *testing.T) {
	collect := map[string]string{"dataset_role": "raw_collection"}
	for _, tc := range []struct {
		name   string
		ds     Dataset
		source *Dataset
		allDay bool
		want   string
	}{
		{name: "全天交易的空间写交易所", ds: Dataset{DatasetID: "dataset_x", DataSourceID: "Binance_Futures", Freq: "1h", Attributes: map[string]string{"dataset_role": "raw_collection", "market_type": "swap"}}, allDay: true, want: "binance-futures-swap-kline-1h"},
		{name: "有收盘时刻的空间不写交易所", ds: Dataset{DatasetID: "dataset_x", DataSourceID: "eastmoney", Freq: "1d", Attributes: map[string]string{"dataset_role": "raw_collection", "market_type": "equity"}}, want: "equity-kline-1d"},
		{name: "标签命中对照表优先于市场类型", ds: Dataset{DatasetID: "dataset_x", Freq: "5m", SubjectTags: []string{"other", "cn_hs300"}, Attributes: map[string]string{"dataset_role": "raw_collection", "market_type": "equity"}}, want: "hs300-kline-5m"},
		{name: "市场类型也查对照表", ds: Dataset{DatasetID: "dataset_x", Freq: "1m", Attributes: map[string]string{"dataset_role": "raw_collection", "market_type": "convertible_bond"}}, want: "bond-kline-1m"},
		{name: "其他用途不加类型", ds: Dataset{DatasetID: "dataset_x", Freq: "1w", Attributes: map[string]string{"market_type": "spot"}}, want: "spot-1w"},
		{name: "月频按规范写法", ds: Dataset{DatasetID: "dataset_x", Freq: "1mo", Attributes: map[string]string{"dataset_role": "raw_collection", "instrument_type": "index"}}, want: "index-kline-1mo"},
		{name: "因子结果取源数据集的品种与交易所", ds: Dataset{DatasetID: "dataset_f", Freq: "15m", Attributes: map[string]string{"dataset_role": "factor_result"}},
			source: &Dataset{DataSourceID: "okx", Freq: "1m", Attributes: map[string]string{"market_type": "spot"}}, allDay: true, want: "okx-spot-15m"},
		{name: "没有品种时回退", ds: Dataset{DatasetID: "dataset_foo__bar_1m", DataSourceID: "binance", Freq: "1m", Attributes: collect}, allDay: true, want: "foo-bar-1m"},
		{name: "频率大小写不规范时回退", ds: Dataset{DatasetID: "month_bars", Freq: "1M", Attributes: map[string]string{"market_type": "spot"}}, want: "month-bars"},
	} {
		source := tc.ds
		if tc.source != nil {
			source = *tc.source
		}
		if got := suggestDirName(tc.ds, source, tc.allDay); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
		if err := validateDirName(suggestDirName(tc.ds, source, tc.allDay)); err != nil {
			t.Errorf("%s: 建议值必须合规: %v", tc.name, err)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "a--b", "A", "a_b", "a/b", ".state", "a.b", strings.Repeat("a", 65)} {
		if validateDirName(bad) == nil {
			t.Errorf("%q 应不合规", bad)
		}
	}
	if validateDirName("binance-spot-kline-1m") != nil {
		t.Error("合规的目录名被拒")
	}
}

func TestDirNameConflictIsReportedNotRenamed(t *testing.T) {
	h := newHarness(t)
	h.datasets.datasets = productionDatasets()
	ctx := context.Background()
	spot := &archivepb.CreateArchiveTaskReq{SpaceId: "crypto", DatasetId: "dataset_dasftksvjhj2jom4vhd0", DirName: "binance-swap-kline-1m"}
	created, _ := h.mgr.CreateArchiveTask(ctx, spot)
	requireOK(t, created.GetRetInfo())
	if created.GetTask().GetDirName() != "binance-swap-kline-1m" || created.GetTask().GetPurpose() != "collect" {
		t.Fatalf("task = %v", created.GetTask())
	}
	// 合约数据集的建议值被现货占了：照常返回建议值并标出冲突，不自动改名。
	prep, _ := h.mgr.PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "crypto", DatasetId: "dataset_dasftksvjhj2jom4vhdg"})
	requireOK(t, prep.GetRetInfo())
	if prep.GetSuggestedDirName() != "binance-swap-kline-1m" || !prep.GetDirNameConflict() {
		t.Fatalf("prepare = %v", prep)
	}
	// 检查用户自己填的目录名。
	custom, _ := h.mgr.PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "crypto", DatasetId: "dataset_dasftksvjhj2jom4vhdg", DirName: "binance-perp-kline-1m"})
	requireOK(t, custom.GetRetInfo())
	if custom.GetSuggestedDirName() != "binance-swap-kline-1m" || custom.GetDirNameConflict() {
		t.Fatalf("prepare custom = %v", custom)
	}
	bad, _ := h.mgr.PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "crypto", DatasetId: "dataset_dasftksvjhj2jom4vhdg", DirName: "Bad_Name"})
	requireCode(t, bad.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "不合规")
	// 不填目录名时取建议值，冲突就拒绝并说明被谁占用。
	swap := &archivepb.CreateArchiveTaskReq{SpaceId: "crypto", DatasetId: "dataset_dasftksvjhj2jom4vhdg"}
	rejected, _ := h.mgr.CreateArchiveTask(ctx, swap)
	requireCode(t, rejected.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "目录名 binance-swap-kline-1m 已被 dataset_dasftksvjhj2jom4vhd0 使用，请改一个")
	invalid, _ := h.mgr.CreateArchiveTask(ctx, &archivepb.CreateArchiveTaskReq{SpaceId: "crypto", DatasetId: "dataset_dasftksvjhj2jom4vhdg", DirName: "a--b"})
	requireCode(t, invalid.GetRetInfo(), commonpb.ErrorCode_INVALID_PARAM, "不合规")
	swap.DirName = "binance-perp-kline-1m"
	created, _ = h.mgr.CreateArchiveTask(ctx, swap)
	requireOK(t, created.GetRetInfo())
	// 同一数据集重复创建仍是“已经有归档任务”。
	again, _ := h.mgr.CreateArchiveTask(ctx, swap)
	requireCode(t, again.GetRetInfo(), commonpb.ErrorCode_CONFLICT, "已经有归档任务")
	// 修改任务不改目录名。
	updated, _ := h.mgr.UpdateArchiveTask(ctx, &archivepb.UpdateArchiveTaskReq{SpaceId: "crypto", DatasetId: "dataset_dasftksvjhj2jom4vhdg", Status: "paused"})
	requireOK(t, updated.GetRetInfo())
	if updated.GetTask().GetDirName() != "binance-perp-kline-1m" {
		t.Fatalf("task = %v", updated.GetTask())
	}
}

type flakySource struct {
	*fakeDatasets
	failID string
}

func (f flakySource) GetDataset(ctx context.Context, space, dataset string) (Dataset, error) {
	if dataset == f.failID {
		return Dataset{}, errors.New("connection reset")
	}
	return f.fakeDatasets.GetDataset(ctx, space, dataset)
}

// 读不到因子结果的源数据集时报错，不用回退名把目录永久定下来；源数据集已删除时按回退规则。
func TestFactorResultDirNameNeedsTheSourceDataset(t *testing.T) {
	h := newHarness(t)
	h.datasets.datasets = productionDatasets()
	svc, err := New(h.store, h.svc.cfg, flakySource{fakeDatasets: h.datasets, failID: "dataset_dasftksvjhj2jom4vhd0"}, WithClock(func() time.Time { return h.now }))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	prep, _ := svc.Mgr().PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "crypto", DatasetId: "dataset_factor_spot_1m"})
	requireCode(t, prep.GetRetInfo(), commonpb.ErrorCode_INNER_ERR, "读取因子结果的源数据集 dataset_dasftksvjhj2jom4vhd0 失败")
	created, _ := svc.Mgr().CreateArchiveTask(ctx, &archivepb.CreateArchiveTaskReq{SpaceId: "crypto", DatasetId: "dataset_factor_spot_1m"})
	requireCode(t, created.GetRetInfo(), commonpb.ErrorCode_INNER_ERR, "源数据集")
	// 用户自己填了目录名时不需要读源数据集。
	created, _ = svc.Mgr().CreateArchiveTask(ctx, &archivepb.CreateArchiveTaskReq{SpaceId: "crypto", DatasetId: "dataset_factor_spot_1m", DirName: "binance-spot-1m"})
	requireOK(t, created.GetRetInfo())

	delete(h.datasets.datasets, "crypto/dataset_dasftksvjhj2jom4vhdg")
	gone, _ := h.mgr.PrepareArchiveTask(ctx, &archivepb.PrepareArchiveTaskReq{SpaceId: "crypto", DatasetId: "dataset_factor_swap_1m"})
	requireOK(t, gone.GetRetInfo())
	if gone.GetPurpose() != "factor" || gone.GetSuggestedDirName() != "factor-swap-1m" {
		t.Fatalf("源数据集已删除时回退: %v", gone)
	}
}
