// Package pebble implements the online ordered KV storage adapter with
// CockroachDB Pebble while preserving the adapter DAO contract used by the
// legacy storage path.
package pebble

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	pebblekv "github.com/cockroachdb/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/config"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

type Pebble struct {
	db             *pebblekv.DB
	tableID        string
	writeOptions   *pebblekv.WriteOptions
	actualConnPath string
}

var createTableLocks = struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}{
	locks: make(map[string]*sync.Mutex),
}

func init() {
	dao.RegisterDeviceType(pb.EnumDeviceType_PEBBLE_DEVICE, func(ctx context.Context) (dao.Storer, error) {
		return NewPebble(ctx), nil
	})
}

func NewPebble(_ context.Context) *Pebble {
	return &Pebble{
		writeOptions: pebblekv.Sync,
	}
}

func (p *Pebble) GetDeviceTableID(logicTableID string) string {
	return logicTableID
}

func (p *Pebble) GetDeviceConn(connectInfo string) error {
	ctx := context.Background()
	actualConnectInfo := connectInfo
	if connectInfo == "localhost" {
		cfg := config.GetGlobalConfig()
		if cfg != nil && cfg.Pebble.DataPath != "" {
			actualConnectInfo = cfg.Pebble.DataPath
		} else {
			actualConnectInfo = "../database/pebble"
		}
	}
	if absPath, err := filepath.Abs(actualConnectInfo); err == nil {
		actualConnectInfo = absPath
	}
	if err := os.MkdirAll(actualConnectInfo, 0755); err != nil {
		log.ErrorContextf(ctx, "创建Pebble数据目录失败: %v", err)
		return errs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV), fmt.Sprintf("创建Pebble数据目录失败: %v", err))
	}

	db, err := pebblekv.Open(actualConnectInfo, &pebblekv.Options{})
	if err != nil {
		log.ErrorContextf(ctx, "连接Pebble失败: %v", err)
		return errs.New(int(pb.EnumErrorCode_FAILED_CONNECT_DEV), fmt.Sprintf("连接Pebble失败: %v", err))
	}

	p.db = db
	p.actualConnPath = actualConnectInfo
	log.InfoContextf(ctx, "Pebble连接成功，实际连接信息: %s", actualConnectInfo)
	return nil
}

func (p *Pebble) GetDeviceKey() string {
	return "pebble"
}

func (p *Pebble) GetActualConnPath() string {
	return p.actualConnPath
}

func (p *Pebble) CloseDeviceConn() error {
	if p.db == nil {
		return nil
	}
	err := p.db.Close()
	p.db = nil
	return err
}

func (p *Pebble) SetFieldInfos(ctx context.Context, params *dao.SetFieldParams) (*pb.SetFieldInfosRsp, error) {
	log.DebugContextf(ctx, "Pebble SetFieldInfos: %+v", params)
	if params == nil {
		return nil, fmt.Errorf("set field params is nil")
	}
	if len(params.UpdateDocRows) == 0 {
		return setSuccessRsp(), nil
	}

	p.tableID = params.TableID
	switch params.DataType {
	case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
		return p.processStaticDataUpdate(ctx, params)
	case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
		return p.processTimingDataUpdate(ctx, params)
	default:
		return nil, fmt.Errorf("invalid data type: %v", params.DataType)
	}
}

func (p *Pebble) GetFieldInfos(ctx context.Context, params *dao.GetFieldParams) ([]*pb.DocRow, error) {
	if params == nil {
		return nil, fmt.Errorf("get field params is nil")
	}
	p.tableID = params.TableID
	switch params.DataType {
	case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
		return p.GetStaticFieldInfos(ctx, params)
	case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
		return p.GetTimingFieldInfos(ctx, params)
	default:
		return nil, fmt.Errorf("invalid data type")
	}
}

func (p *Pebble) SearchFieldInfos(ctx context.Context, params *dao.SearchFieldParams) ([]*pb.DocRow, uint64, error) {
	log.DebugContextf(ctx, "Pebble SearchFieldInfos: %+v", params)
	if params == nil {
		return nil, 0, fmt.Errorf("search field params is nil")
	}
	p.tableID = params.TableID
	switch params.DataType {
	case pb.EnumDataTypeCategory_STATIC_DATA_TYPE:
		return p.SearchStaticFieldInfos(ctx, params)
	case pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE:
		return p.SearchTimingFieldInfos(ctx, params)
	default:
		return nil, 0, fmt.Errorf("invalid data type")
	}
}

func (p *Pebble) DeleteRows(ctx context.Context, params *dao.DeleteRowsParams) (*pb.DeleteRowsRsp, error) {
	rsp := &pb.DeleteRowsRsp{
		RetInfo:      successRetInfo(),
		DeletedCount: 0,
	}
	if params == nil {
		return nil, fmt.Errorf("delete rows params is nil")
	}
	p.tableID = params.TableID
	if params.DataType == pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE {
		return nil, fmt.Errorf("时序数据不支持删除操作")
	}
	if params.DataType != pb.EnumDataTypeCategory_STATIC_DATA_TYPE {
		return nil, fmt.Errorf("invalid data type")
	}
	if len(params.RowIDs) == 0 {
		return rsp, nil
	}

	batch := p.db.NewBatch()
	defer batch.Close()

	deletedCount := uint64(0)
	currentTime := time.Now().Format("2006-01-02 15:04:05")
	for _, rowID := range params.RowIDs {
		deleted, err := p.isRowDeleted(params.TableID, rowID)
		if err != nil {
			log.ErrorContextf(ctx, "检查删除状态失败: %v", err)
			continue
		}
		if deleted {
			continue
		}
		if err := batch.Set([]byte(buildDeletedKey(params.TableID, rowID)), []byte("1"), nil); err != nil {
			return nil, err
		}
		if err := batch.Set([]byte(buildDeletedTimeKey(params.TableID, rowID)), []byte(currentTime), nil); err != nil {
			return nil, err
		}
		deletedCount++
	}
	if err := batch.Commit(p.writeOptions); err != nil {
		return nil, errs.New(int(pb.EnumErrorCode_INNER_ERR), fmt.Sprintf("删除操作失败: %v", err))
	}
	rsp.DeletedCount = deletedCount
	return rsp, nil
}
