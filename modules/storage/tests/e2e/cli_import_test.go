//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func testCLIStorageImportCSV(ctx context.Context, t *testing.T) {
	meta := harness.MetadataClient()
	mustSuccess(t, "UpsertSubject:cli-import", func() *pb.RetInfo {
		rsp, err := meta.UpsertSubject(ctx, &pb.UpsertSubjectReq{Subject: &pb.Subject{
			SpaceId:     e2eSpaceID,
			SubjectId:   cliSubjectID,
			SubjectType: "crypto_pair",
			Name:        cliSubjectID,
			Market:      "crypto",
			Status:      "active",
		}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})
	mustSuccess(t, "CreateDataset:cli-import", func() *pb.RetInfo {
		rsp, err := meta.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{
			SpaceId:      e2eSpaceID,
			DatasetId:    cliDatasetID,
			DataSourceId: dataSourceID,
			Name:         "CLI 导入 K 线 1h",
			DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
			Freqs:        []string{freq},
			Status:       "active",
		}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})
	mustSuccess(t, "UpsertDatasetColumn:cli-import:close", func() *pb.RetInfo {
		rsp, err := meta.UpsertDatasetColumn(ctx, &pb.UpsertDatasetColumnReq{Column: &pb.DatasetColumn{
			SpaceId:    e2eSpaceID,
			DatasetId:  cliDatasetID,
			ColumnName: "close",
			OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD,
			OriginId:   "close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Status:     "active",
		}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})
	mustSuccess(t, "CreatePrimaryStoreRoute:cli-import", func() *pb.RetInfo {
		rsp, err := meta.CreatePrimaryStoreRoute(ctx, &pb.CreatePrimaryStoreRouteReq{PrimaryStoreRoute: &pb.PrimaryStoreRoute{
			SpaceId:        e2eSpaceID,
			DatasetId:      cliDatasetID,
			SubjectPattern: "*",
			NodeId:         "node_pebble",
			Status:         "active",
		}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})

	rows := klines(t)
	first := rows[0]
	updatedClose := first.Close + 123.456
	csvPath := filepath.Join(t.TempDir(), "AR-USDT-import.csv")
	content := fmt.Sprintf("downloaded from exchange\ncandle_begin_time,close\n%s,%.8f\n",
		first.Time.UTC().Format(csvTimeLayout), updatedClose)
	require.NoError(t, os.WriteFile(csvPath, []byte(content), 0o644))

	cliDir := filepath.Clean(filepath.Join(harness.moduleDir, "..", "cli"))
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/moox-cli",
		"storage", "import",
		"--format", "csv",
		"--file", csvPath,
		"--access-url", fmt.Sprintf("http://127.0.0.1:%d", portDataHTTP),
		"--metadata-url", fmt.Sprintf("http://127.0.0.1:%d", portMetadataHTTP),
		"--space", e2eSpaceID,
		"--dataset", cliDatasetID,
		"--subject", cliSubjectID,
		"--data-source", dataSourceID,
		"--freq", freq,
		"--time-column", "candle_begin_time",
		"--batch-size", "1",
	)
	cmd.Dir = cliDir

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), "stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())

	var summary struct {
		Status        string `json:"status"`
		Format        string `json:"format"`
		ValidatedRows int    `json:"validated_rows"`
		WrittenRows   int    `json:"written_rows"`
		Batches       int    `json:"batches"`
		BoundSubject  bool   `json:"bound_subject"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &summary), "stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	require.Equal(t, "imported", summary.Status)
	require.Equal(t, "csv", summary.Format)
	require.Equal(t, 1, summary.ValidatedRows)
	require.Equal(t, 1, summary.WrittenRows)
	require.Equal(t, 1, summary.Batches)
	require.True(t, summary.BoundSubject, "CLI 导入应在 subject 未绑定时调用 MetadataService 绑定 dataset-subject")

	data := harness.DataClient()
	point := first.Time.UTC().Format(time.RFC3339)
	rsp, err := data.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{SpaceId: e2eSpaceID, DatasetId: cliDatasetID, SubjectId: cliSubjectID, Freq: freq}},
		TimeRange: &pb.TimeRange{
			StartTime: point,
			EndTime:   point,
		},
		ColumnNames: []string{"close"},
		Page:        &pb.Page{Size: 1},
	})
	require.NoError(t, err)
	requireSuccess(t, rsp.GetRetInfo())
	require.Len(t, rsp.GetRows(), 1)
	require.InDelta(t, updatedClose, columnDouble(rsp.GetRows()[0], "close"), 1e-8, "CLI 导入应按同一 data key 更新主存 close 列")
}
