package retinfo

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/storeerr"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
)

func TestSuccessUsesDefaultMessage(t *testing.T) {
	got := Success("")
	assert.Equal(t, pb.ErrorCode_SUCCESS, got.GetCode())
	assert.Equal(t, "success", got.GetMsg())
}

func TestErrorIncludesMessageWhenPresent(t *testing.T) {
	got := Error(pb.ErrorCode_INVALID_PARAM, errors.New("bad input"))
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, got.GetCode())
	assert.Equal(t, "bad input", got.GetMsg())

	empty := Error(pb.ErrorCode_INNER_ERR, nil)
	assert.Equal(t, pb.ErrorCode_INNER_ERR, empty.GetCode())
	assert.Empty(t, empty.GetMsg())
}

func TestCodeMapsStoreErrorKinds(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want pb.ErrorCode
	}{
		{name: "参数错误", err: storeerr.Invalidf("dataset name is required"), want: pb.ErrorCode_INVALID_PARAM},
		{name: "包装后的参数错误", err: fmt.Errorf("upsert: %w", storeerr.Invalidf("数据集的 alignment 设置后不能修改")), want: pb.ErrorCode_INVALID_PARAM},
		{name: "不存在", err: storeerr.NotFoundf("dataset %s does not exist", "d"), want: pb.ErrorCode_NOT_FOUND},
		{name: "数据库未找到", err: fmt.Errorf("metadata row not found: %w", sql.ErrNoRows), want: pb.ErrorCode_NOT_FOUND},
		{name: "冲突", err: storeerr.Conflictf("view already exists"), want: pb.ErrorCode_CONFLICT},
		// 没有标类别的错误不看文字：即使写着 is required 也是内部错误。
		{name: "未标类别", err: errors.New("space_id is required"), want: pb.ErrorCode_INNER_ERR},
		{name: "成功", err: nil, want: pb.ErrorCode_SUCCESS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Code(tt.err))
		})
	}
}
