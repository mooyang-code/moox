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
	client := &recordingMetadataClient{sourceDatasets: map[string]string{"binance_spot_kline": "binance"}}
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
		"UpsertDatasetColumn:Bias_20",
		"UpsertDatasetColumn:Bias_96",
	}
	if !reflect.DeepEqual(client.calls, want) {
		t.Fatalf("calls = %#v, want %#v", client.calls, want)
	}
	if got := client.datasetReqs[0].GetDataset().GetDataSourceId(); got != "binance" {
		t.Fatalf("data source id = %q", got)
	}
	if got := client.datasetReqs[0].GetDataset().GetName(); got == "" || got == "因子结果" || len([]rune(got)) > 10 || !strings.Contains(got, "因子") {
		t.Fatalf("dataset name = %q", got)
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

type recordingMetadataClient struct {
	calls          []string
	sourceDatasets map[string]string
	datasetReqs    []*storagepb.CreateDatasetReq
	columnReqs     []*storagepb.UpsertDatasetColumnReq
}

func (c *recordingMetadataClient) CreateFactor(_ context.Context, req *storagepb.CreateFactorReq) (*storagepb.CreateFactorRsp, error) {
	c.calls = append(c.calls, "CreateFactor:"+req.GetFactor().GetFactorId())
	return &storagepb.CreateFactorRsp{RetInfo: successRet(), Factor: req.GetFactor()}, nil
}

func (c *recordingMetadataClient) CreateDataset(_ context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	c.calls = append(c.calls, "CreateDataset:"+req.GetDataset().GetDatasetId())
	c.datasetReqs = append(c.datasetReqs, req)
	return &storagepb.CreateDatasetRsp{RetInfo: successRet(), Dataset: req.GetDataset()}, nil
}

func (c *recordingMetadataClient) UpsertDatasetColumn(_ context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	c.calls = append(c.calls, "UpsertDatasetColumn:"+req.GetColumn().GetColumnName())
	c.columnReqs = append(c.columnReqs, req)
	return &storagepb.UpsertDatasetColumnRsp{RetInfo: successRet(), Column: req.GetColumn()}, nil
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
