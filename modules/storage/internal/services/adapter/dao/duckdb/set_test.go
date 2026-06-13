package duckdb

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
)

// TestBuildStaticUpsertQuery 测试静态数据upsert查询构建
func TestBuildStaticUpsertQuery(t *testing.T) {
	d := &DuckDB{}

	// 测试数据
	tableID := "test_table"
	columns := []string{"_row_id", "_times", "_ctime", "_mtime", "field_100", "field_200"}
	placeholders := []string{"(?, ?, ?, ?, ?, ?)"}
	userColumns := []string{"field_100"} // 用户只更新field_100

	// 调用函数
	query := d.buildStaticUpsertQuery(tableID, columns, placeholders, userColumns)

	// 验证结果
	expectedQuery := "INSERT INTO test_table (_row_id, _times, _ctime, _mtime, field_100, field_200) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (_row_id) DO NOTHING"
	assert.Equal(t, expectedQuery, query)

	// 当前实现通过 DO NOTHING 避免重复更新。
	assert.Contains(t, query, "ON CONFLICT (_row_id)")
	assert.Contains(t, query, "DO NOTHING")
	assert.NotContains(t, query, "DO UPDATE")
}

// TestBuildTimingUpsertQuery 测试时序数据upsert查询构建
func TestBuildTimingUpsertQuery(t *testing.T) {
	d := &DuckDB{}

	// 测试数据
	tableID := "test_table"
	columns := []string{"_row_id", "_times", "_ctime", "_mtime", "field_100", "field_200"}
	placeholders := []string{"(?, ?, ?, ?, ?, ?)"}
	userColumns := []string{"field_200"} // 用户只更新field_200

	// 调用函数
	query := d.buildTimingUpsertQuery(tableID, columns, placeholders, userColumns)

	// 验证结果
	expectedQuery := "INSERT INTO test_table (_row_id, _times, _ctime, _mtime, field_100, field_200) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (_times) DO NOTHING"
	assert.Equal(t, expectedQuery, query)

	// 当前实现通过 DO NOTHING 避免重复更新。
	assert.Contains(t, query, "ON CONFLICT (_times)")
	assert.Contains(t, query, "DO NOTHING")
	assert.NotContains(t, query, "DO UPDATE")
}

// TestCollectUserColumns 测试用户字段收集
func TestCollectUserColumns(t *testing.T) {
	d := &DuckDB{
		tableID: "test_table",
	}

	// 模拟docInfo数据
	batch := []*docInfo{
		{
			insertFields: map[uint32]*pb.FieldInfo{
				100: {}, // field_100
				200: {}, // field_200
			},
		},
	}

	// 由于需要调用getFieldType和formatColumnName，这里只测试函数结构
	// 在实际环境中需要mock这些依赖
	ctx := context.Background()

	// 这个测试需要完整的DuckDB实例和字段映射，暂时跳过
	t.Skip("需要完整的DuckDB实例和字段映射才能测试")

	columns := d.collectUserColumns(ctx, batch)

	// 验证只包含用户字段，不包含系统字段
	assert.NotContains(t, columns, "_row_id")
	assert.NotContains(t, columns, "_times")
	assert.NotContains(t, columns, "_ctime")
	assert.NotContains(t, columns, "_mtime")
}
