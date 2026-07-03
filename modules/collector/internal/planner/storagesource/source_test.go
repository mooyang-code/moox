package storagesource

import (
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestMergeDatasetSubjects(t *testing.T) {
	bindings := []*storagepb.DatasetSubject{
		{
			SubjectId: "BTC-USDT",
			Status:    "active",
			Attributes: map[string]string{
				"external_symbol": "BTCUSDT_FROM_BINDING",
			},
		},
		{
			SubjectId: "ETH-USDT",
			Status:    "",
		},
		{
			SubjectId: "BNB-USDT",
			Status:    "disabled",
		},
		{
			SubjectId: "SOL-USDT",
			Status:    "enabled",
		},
	}
	symbols := map[string]string{
		"BTC-USDT": "BTCUSDT_FROM_SYMBOLS",
		"ETH-USDT": "ETHUSDT",
	}

	got := mergeDatasetSubjects(bindings, symbols)
	want := []domain.DatasetSubject{
		{
			SubjectID:      "BTC-USDT",
			SubjectName:    "BTC-USDT",
			ExternalSymbol: "BTCUSDT_FROM_BINDING",
			Status:         "active",
		},
		{
			SubjectID:      "ETH-USDT",
			SubjectName:    "ETH-USDT",
			ExternalSymbol: "ETHUSDT",
			Status:         "",
		},
		{
			SubjectID:      "SOL-USDT",
			SubjectName:    "SOL-USDT",
			ExternalSymbol: "SOL-USDT",
			Status:         "enabled",
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeDatasetSubjects() = %#v, want %#v", got, want)
	}
}

func TestNormalizeTRPCTargetDoesNotConvertHTTP(t *testing.T) {
	got := normalizeTRPCTarget("http://127.0.0.1:20200", "20100")
	if got != "http://127.0.0.1:20200" {
		t.Fatalf("normalizeTRPCTarget() = %q, want raw HTTP target to remain unconverted", got)
	}
}
