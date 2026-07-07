package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/repository"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"gorm.io/gorm"
)

func TestImportFactorFileDefaults(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := "def signal(*args):\n    return args[0]\n"
	path := filepath.Join(dir, "Bias.py")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write factor: %v", err)
	}
	db := openRegistryTestDB(t)
	svc := NewService(repository.NewFactorRepository(db), nil, Options{
		FactorsDir:    dir,
		DefaultParams: []int{20},
	})

	factor, err := svc.ImportFactorFile(ctx, path)
	if err != nil {
		t.Fatalf("ImportFactorFile() error = %v", err)
	}

	sum := sha256.Sum256([]byte(source))
	if factor.FactorID != "bias" || factor.Name != "Bias" {
		t.Fatalf("unexpected factor identity: %+v", factor)
	}
	if factor.SourceHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("source hash = %q", factor.SourceHash)
	}
	if factor.ParamsJSON != "[20]" {
		t.Fatalf("params_json = %q", factor.ParamsJSON)
	}
	if factor.LookbackBars != 200 {
		t.Fatalf("lookback = %d, want minimum 200", factor.LookbackBars)
	}
}

func TestDefaultLookback(t *testing.T) {
	tests := []struct {
		params []int
		want   int
	}{
		{params: nil, want: 200},
		{params: []int{20}, want: 200},
		{params: []int{20, 96, 288}, want: 864},
	}
	for _, tt := range tests {
		if got := DefaultLookback(tt.params); got != tt.want {
			t.Fatalf("DefaultLookback(%v) = %d, want %d", tt.params, got, tt.want)
		}
	}
}

func TestResultDataset(t *testing.T) {
	if got := ResultDataset("binance_spot_kline"); got != "binance_spot_factor" {
		t.Fatalf("ResultDataset(kline) = %q", got)
	}
	got := ResultDataset("custom_dataset")
	if len(got) > 20 || !strings.HasPrefix(got, "custom_dataset_f") {
		t.Fatalf("ResultDataset(custom) = %q, want <=20 chars with stable hash suffix", got)
	}
}

func TestExtraColumnsFromSource(t *testing.T) {
	source := `
extra_data_dict = {
    'coin-cap': ['circulating_supply', 'max_supply'],
}
`
	got := ExtraColumnsFromSource(source)
	want := []string{"circulating_supply", "max_supply"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtraColumnsFromSource() = %#v, want %#v", got, want)
	}
	factors := []domain.FactorDef{{DependsJSON: DependsJSONFromSource(source)}}
	if got := ExtraColumnsFromFactors(factors); !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtraColumnsFromFactors() = %#v, want %#v", got, want)
	}
}

func TestMetadataSyncOrderAndPayload(t *testing.T) {
	ctx := context.Background()
	client := &recordingMetadataClient{
		sourceDatasets: map[string]string{"binance_spot_kline": "binance"},
		sourceSubjects: map[string][]string{"binance_spot_kline": []string{"BTC-USDT", "ETH-USDT"}},
		sourceSubjectItems: map[string][]*storagepb.DatasetSubject{"binance_spot_kline": {
			{SubjectId: "BTC-USDT", SubjectRole: "normal", Status: "active", Attributes: map[string]string{"market": "spot"}},
			{SubjectId: "ETH-USDT", SubjectRole: "normal", Status: "active"},
		}},
		sourceRoutes: map[string][]*storagepb.PrimaryStoreRoute{"binance_spot_kline": {
			{
				SpaceId:        "crypto",
				DatasetId:      "binance_spot_kline",
				SubjectPattern: "*",
				HashRule:       "subject_id",
				NodeId:         "local",
				Priority:       100,
				Status:         "active",
				Attributes:     map[string]string{"shard": "hot"},
			},
		}},
	}
	syncer := NewMetadataSync(client, nil)
	factors := []domain.FactorDef{
		{
			FactorID:      "bias",
			Name:          "Bias",
			ParamsJSON:    "[20,96]",
			LookbackBars:  288,
			WritebackBars: 5,
			Status:        domain.FactorStatusEnabled,
		},
	}

	if err := syncer.SyncResultDataset(ctx, "crypto", "binance_spot_kline", "1m", factors); err != nil {
		t.Fatalf("SyncResultDataset() error = %v", err)
	}

	want := []string{
		"CreateFactor:bias",
		"CreateDataset:binance_spot_factor",
		"ListPrimaryStoreRoutes:binance_spot_kline",
		"CreatePrimaryStoreRoute:binance_spot_factor/*",
		"ListDatasetSubjects:binance_spot_kline",
		"BindDatasetSubject:binance_spot_factor/BTC-USDT",
		"BindDatasetSubject:binance_spot_factor/ETH-USDT",
		"UpsertDatasetColumn:Bias_20",
		"UpsertDatasetColumn:Bias_96",
	}
	if !reflect.DeepEqual(client.calls, want) {
		t.Fatalf("calls = %#v, want %#v", client.calls, want)
	}
	if got := client.datasetReqs[0].GetDataset().GetDataSourceId(); got != "binance" {
		t.Fatalf("data source id = %q", got)
	}
	if got := client.factorReqs[0].GetFactor().GetStatus(); got != "active" {
		t.Fatalf("factor metadata status = %q, want active", got)
	}
	if got := client.datasetReqs[0].GetDataset().GetName(); got == "" || got == "因子结果" || len([]rune(got)) > 10 || !strings.Contains(got, "因子") {
		t.Fatalf("dataset name = %q", got)
	}
	if len(client.routeReqs) != 1 {
		t.Fatalf("route calls = %d, want 1", len(client.routeReqs))
	}
	if got := client.routeReqs[0].GetPrimaryStoreRoute(); got.GetDatasetId() != "binance_spot_factor" || got.GetSubjectPattern() != "*" || got.GetNodeId() != "local" {
		t.Fatalf("route payload = %+v", got)
	}
	if got := client.routeReqs[0].GetPrimaryStoreRoute().GetAttributes()["shard"]; got != "hot" {
		t.Fatalf("route attributes were not copied: %+v", client.routeReqs[0].GetPrimaryStoreRoute().GetAttributes())
	}
	for i, req := range client.bindReqs {
		got := req.GetDatasetSubject()
		if got.GetSpaceId() != "crypto" || got.GetDatasetId() != "binance_spot_factor" {
			t.Fatalf("binding[%d] target = %+v", i, got)
		}
		if got.GetSubjectRole() != "normal" || got.GetStatus() != "active" {
			t.Fatalf("binding[%d] metadata = %+v", i, got)
		}
	}
	if got := client.bindReqs[0].GetDatasetSubject().GetAttributes()["market"]; got != "spot" {
		t.Fatalf("dataset subject attributes were not copied: %+v", client.bindReqs[0].GetDatasetSubject().GetAttributes())
	}
	for i, req := range client.columnReqs {
		col := req.GetColumn()
		if col.GetValueType() != storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE {
			t.Fatalf("value type = %v", col.GetValueType())
		}
		if col.GetOriginType() != storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR {
			t.Fatalf("origin type = %v", col.GetOriginType())
		}
		wantOriginID := []string{"bias_20", "bias_96"}[i]
		if col.GetOriginId() != wantOriginID {
			t.Fatalf("origin id = %q", col.GetOriginId())
		}
		if col.GetAttributes()["display_name"] == "" {
			t.Fatalf("missing display name: %+v", col.GetAttributes())
		}
	}
}

func TestMetadataSyncOnlyTreatsDuplicateConstraintAsDuplicate(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{name: "unique constraint", msg: "UNIQUE constraint failed: t_factors.c_space_id, t_factors.c_factor_id", want: true},
		{name: "already exists", msg: "dataset already exists", want: true},
		{name: "check constraint", msg: "constraint failed: CHECK constraint failed: c_status IN ('active', 'disabled')", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ret := &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: tt.msg}
			if got := isDuplicateRet(ret); got != tt.want {
				t.Fatalf("isDuplicateRet(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

func TestStorageFactorStatus(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   string
	}{
		{name: "enabled", status: domain.FactorStatusEnabled, want: "active"},
		{name: "disabled", status: domain.FactorStatusDisabled, want: "disabled"},
		{name: "empty", status: "", want: "active"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := storageFactorStatus(tt.status); got != tt.want {
				t.Fatalf("storageFactorStatus(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestMetadataSyncUsesSourceDatasetDataSourceID(t *testing.T) {
	ctx := context.Background()
	client := &recordingMetadataClient{sourceDatasets: map[string]string{"em_daily_kline": "eastmoney"}}
	syncer := NewMetadataSync(client, nil)
	factors := []domain.FactorDef{{
		FactorID:      "bias",
		Name:          "Bias",
		ParamsJSON:    "[20]",
		LookbackBars:  288,
		WritebackBars: 5,
		Status:        domain.FactorStatusEnabled,
	}}

	if err := syncer.SyncResultDataset(ctx, "stock", "em_daily_kline", "1d", factors); err != nil {
		t.Fatalf("SyncResultDataset() error = %v", err)
	}
	if got := client.datasetReqs[0].GetDataset().GetDataSourceId(); got != "eastmoney" {
		t.Fatalf("data source id = %q", got)
	}
}

func TestMetadataSyncTreatsConcurrentRouteRefreshAsBound(t *testing.T) {
	ctx := context.Background()
	client := &recordingMetadataClient{
		sourceDatasets: map[string]string{"binance_spot_kline": "binance"},
		sourceSubjects: map[string][]string{"binance_spot_kline": []string{"BTC-USDT"}},
		sourceRoutes: map[string][]*storagepb.PrimaryStoreRoute{"binance_spot_kline": {
			{SpaceId: "crypto", DatasetId: "binance_spot_kline", SubjectPattern: "*", NodeId: "local", Priority: 100, Status: "active"},
		}},
		bindRet: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "snapshotcache: refresh already in progress"},
	}
	syncer := NewMetadataSync(client, nil)
	factors := []domain.FactorDef{{
		FactorID:      "bias",
		Name:          "Bias",
		ParamsJSON:    "[20]",
		LookbackBars:  288,
		WritebackBars: 5,
		Status:        domain.FactorStatusEnabled,
	}}

	if err := syncer.SyncResultDataset(ctx, "crypto", "binance_spot_kline", "1m", factors); err != nil {
		t.Fatalf("SyncResultDataset() error = %v", err)
	}
	if len(client.bindReqs) != 1 {
		t.Fatalf("bind calls = %d, want 1", len(client.bindReqs))
	}
}

func TestMetadataSyncTreatsConcurrentMetadataRefreshAsApplied(t *testing.T) {
	ctx := context.Background()
	refreshing := &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "snapshotcache: refresh already in progress"}
	client := &recordingMetadataClient{
		sourceDatasets: map[string]string{"binance_spot_kline": "binance"},
		factorRet:      refreshing,
		datasetRet:     refreshing,
		columnRet:      refreshing,
	}
	syncer := NewMetadataSync(client, nil)
	factors := []domain.FactorDef{{
		FactorID:      "bias",
		Name:          "Bias",
		ParamsJSON:    "[20]",
		LookbackBars:  288,
		WritebackBars: 5,
		Status:        domain.FactorStatusEnabled,
	}}

	if err := syncer.SyncResultDataset(ctx, "crypto", "binance_spot_kline", "1m", factors); err != nil {
		t.Fatalf("SyncResultDataset() error = %v", err)
	}
	want := []string{
		"CreateFactor:bias",
		"CreateDataset:binance_spot_factor",
		"ListPrimaryStoreRoutes:binance_spot_kline",
		"ListDatasetSubjects:binance_spot_kline",
		"UpsertDatasetColumn:Bias_20",
	}
	if !reflect.DeepEqual(client.calls, want) {
		t.Fatalf("calls = %#v, want %#v", client.calls, want)
	}
}

type recordingMetadataClient struct {
	calls              []string
	sourceDatasets     map[string]string
	sourceSubjects     map[string][]string
	sourceSubjectItems map[string][]*storagepb.DatasetSubject
	sourceRoutes       map[string][]*storagepb.PrimaryStoreRoute
	factorRet          *commonpb.RetInfo
	datasetRet         *commonpb.RetInfo
	columnRet          *commonpb.RetInfo
	bindRet            *commonpb.RetInfo
	factorReqs         []*storagepb.CreateFactorReq
	datasetReqs        []*storagepb.CreateDatasetReq
	columnReqs         []*storagepb.UpsertDatasetColumnReq
	bindReqs           []*storagepb.BindDatasetSubjectReq
	routeReqs          []*storagepb.CreatePrimaryStoreRouteReq
}

func (c *recordingMetadataClient) CreateFactor(_ context.Context, req *storagepb.CreateFactorReq) (*storagepb.CreateFactorRsp, error) {
	c.calls = append(c.calls, "CreateFactor:"+req.GetFactor().GetFactorId())
	c.factorReqs = append(c.factorReqs, req)
	ret := c.factorRet
	if ret == nil {
		ret = successRet()
	}
	return &storagepb.CreateFactorRsp{RetInfo: ret, Factor: req.GetFactor()}, nil
}

func (c *recordingMetadataClient) CreateDataset(_ context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	c.calls = append(c.calls, "CreateDataset:"+req.GetDataset().GetDatasetId())
	c.datasetReqs = append(c.datasetReqs, req)
	ret := c.datasetRet
	if ret == nil {
		ret = successRet()
	}
	return &storagepb.CreateDatasetRsp{RetInfo: ret, Dataset: req.GetDataset()}, nil
}

func (c *recordingMetadataClient) UpsertDatasetColumn(_ context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	c.calls = append(c.calls, "UpsertDatasetColumn:"+req.GetColumn().GetColumnName())
	c.columnReqs = append(c.columnReqs, req)
	ret := c.columnRet
	if ret == nil {
		ret = successRet()
	}
	return &storagepb.UpsertDatasetColumnRsp{RetInfo: ret, Column: req.GetColumn()}, nil
}

func (c *recordingMetadataClient) ListDatasetSubjects(_ context.Context, req *storagepb.ListDatasetSubjectsReq) (*storagepb.ListDatasetSubjectsRsp, error) {
	c.calls = append(c.calls, "ListDatasetSubjects:"+req.GetDatasetId())
	if subjects := c.sourceSubjectItems[req.GetDatasetId()]; len(subjects) > 0 {
		out := make([]*storagepb.DatasetSubject, 0, len(subjects))
		for _, subject := range subjects {
			item := *subject
			item.SpaceId = req.GetSpaceId()
			item.DatasetId = req.GetDatasetId()
			out = append(out, &item)
		}
		return &storagepb.ListDatasetSubjectsRsp{RetInfo: successRet(), DatasetSubjects: out}, nil
	}
	subjects := c.sourceSubjects[req.GetDatasetId()]
	out := make([]*storagepb.DatasetSubject, 0, len(subjects))
	for _, subject := range subjects {
		out = append(out, &storagepb.DatasetSubject{
			SpaceId:     req.GetSpaceId(),
			DatasetId:   req.GetDatasetId(),
			SubjectId:   subject,
			SubjectRole: "normal",
			Status:      "active",
		})
	}
	return &storagepb.ListDatasetSubjectsRsp{RetInfo: successRet(), DatasetSubjects: out}, nil
}

func (c *recordingMetadataClient) BindDatasetSubject(_ context.Context, req *storagepb.BindDatasetSubjectReq) (*storagepb.BindDatasetSubjectRsp, error) {
	item := req.GetDatasetSubject()
	c.calls = append(c.calls, "BindDatasetSubject:"+item.GetDatasetId()+"/"+item.GetSubjectId())
	c.bindReqs = append(c.bindReqs, req)
	ret := c.bindRet
	if ret == nil {
		ret = successRet()
	}
	return &storagepb.BindDatasetSubjectRsp{RetInfo: ret, DatasetSubject: item}, nil
}

func (c *recordingMetadataClient) ListPrimaryStoreRoutes(_ context.Context, req *storagepb.ListPrimaryStoreRoutesReq) (*storagepb.ListPrimaryStoreRoutesRsp, error) {
	c.calls = append(c.calls, "ListPrimaryStoreRoutes:"+req.GetDatasetId())
	return &storagepb.ListPrimaryStoreRoutesRsp{RetInfo: successRet(), PrimaryStoreRoutes: c.sourceRoutes[req.GetDatasetId()]}, nil
}

func (c *recordingMetadataClient) CreatePrimaryStoreRoute(_ context.Context, req *storagepb.CreatePrimaryStoreRouteReq) (*storagepb.CreatePrimaryStoreRouteRsp, error) {
	route := req.GetPrimaryStoreRoute()
	c.calls = append(c.calls, "CreatePrimaryStoreRoute:"+route.GetDatasetId()+"/"+route.GetSubjectPattern())
	c.routeReqs = append(c.routeReqs, req)
	return &storagepb.CreatePrimaryStoreRouteRsp{RetInfo: successRet(), PrimaryStoreRoute: route}, nil
}

func (c *recordingMetadataClient) GetDataset(_ context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	dataSourceID := c.sourceDatasets[req.GetDatasetId()]
	return &storagepb.GetDatasetRsp{
		RetInfo: successRet(),
		Dataset: &storagepb.Dataset{
			SpaceId:      req.GetSpaceId(),
			DatasetId:    req.GetDatasetId(),
			DataSourceId: dataSourceID,
		},
	}, nil
}

func successRet() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}
}

func openRegistryTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Exec(factorschema.AllSQL()).Error; err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}
