package cache

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// TestValidatePerRowCost 量化 Primary 写入校验的元数据查询成本：MetadataValidator 对每一行各调一次 GetDataset
// 与 ListDatasetColumns，而快照的这两个查询都是对全部条目的线性扫描加 proto 解码。对比一个 1000 行请求
// 「每行查一次」与「每个数据集只查一次」的耗时。默认跳过；MOOX_META_BENCH=1 时运行。
func TestValidatePerRowCost(t *testing.T) {
	if os.Getenv("MOOX_META_BENCH") != "1" {
		t.Skip("设置 MOOX_META_BENCH=1 运行")
	}
	ctx := context.Background()
	for _, filler := range []int{2000, 5000, 10000} {
		items := make([]entry, 0, filler+8)
		for i := 0; i < filler; i++ {
			payload, _ := proto.Marshal(&pb.Subject{SpaceId: "crypto", SubjectId: fmt.Sprintf("S%05d-USDT", i), Name: "x"})
			items = append(items, entry{Kind: kindSubject, SpaceID: "crypto", ID: fmt.Sprintf("S%05d-USDT", i), Payload: payload})
		}
		datasetPayload, _ := proto.Marshal(&pb.Dataset{SpaceId: "crypto", DatasetId: "dataset_kline_5m", Freq: "5m", Status: "active", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES})
		items = append(items, entry{Kind: kindDataset, SpaceID: "crypto", ID: "dataset_kline_5m", Payload: datasetPayload})
		for _, name := range []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num"} {
			payload, _ := proto.Marshal(&pb.DatasetColumn{SpaceId: "crypto", DatasetId: "dataset_kline_5m", ColumnName: name, OriginId: name, Status: "active", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
			items = append(items, entry{Kind: kindDatasetColumn, SpaceID: "crypto", ID: name, DatasetID: "dataset_kline_5m", Payload: payload})
		}
		snapshot := &MetadataSnapshot{items: items}
		lookup := func() {
			if _, err := snapshot.GetDataset(ctx, "crypto", "dataset_kline_5m"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := snapshot.ListDatasetColumns(ctx, "crypto", "dataset_kline_5m", &pb.Page{Page: 1, Size: 1000}); err != nil {
				t.Fatal(err)
			}
		}
		began := time.Now()
		for row := 0; row < 1000; row++ {
			lookup()
		}
		perRow := time.Since(began)
		began = time.Now()
		lookup()
		once := time.Since(began)
		t.Logf("元数据 %5d 条：1000 行请求逐行查询 %s，按数据集查一次 %s", len(items), perRow.Round(time.Microsecond), once.Round(time.Microsecond))
	}
}
