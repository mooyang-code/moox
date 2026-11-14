package view

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// DataView 的错误码只按哨兵错误判断：错误文字里带 not found、no such file 的 DuckDB 报错是内部错误，不能让调用方当成“还没就绪”一直重试。
func TestQueryErrorCodeUsesSentinels(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want pb.ErrorCode
	}{
		{"索引没准备好", fmt.Errorf("view index %q is not prepared: %w", "x", errViewIndexNotReady), pb.ErrorCode_VIEW_NOT_READY},
		{"索引文件不存在", fmt.Errorf("open: %w", os.ErrNotExist), pb.ErrorCode_VIEW_NOT_READY},
		{"列没有投影", fmt.Errorf("query: %w", viewindex.ColumnNotProjectedError{Column: "close"}), pb.ErrorCode_VIEW_COLUMN_NOT_FOUND},
		{"DuckDB 列绑定错误", errors.New(`Binder Error: Referenced column "x" not found in FROM clause`), pb.ErrorCode_INNER_ERR},
		{"文字像文件不存在", errors.New("no such file or directory"), pb.ErrorCode_INNER_ERR},
		{"其他", errors.New("disk full"), pb.ErrorCode_INNER_ERR},
	} {
		if got := queryErrorCode(tc.err); got != tc.want {
			t.Errorf("%s: %v，想要 %v", tc.name, got, tc.want)
		}
	}
	if msg := (viewindex.ColumnNotProjectedError{Column: "close"}).Error(); msg != `View column "close" is not projected` {
		t.Fatalf("错误文字 = %q", msg)
	}
}
