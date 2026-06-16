package quantstore

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	devicepebble "github.com/mooyang-code/moox/modules/storage/internal/services/device/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

const defaultRoot = "var/storage"

type Store struct {
	root string
}

var pebbleStores sync.Map

func New(root string) *Store {
	if root == "" {
		root = os.Getenv("MOOX_STORAGE_HOME")
	}
	if root == "" {
		root = defaultRoot
	}
	return &Store{root: root}
}

func (s *Store) Root() string {
	return s.root
}

func Success(msg string) *pb.RetInfo {
	if msg == "" {
		msg = "success"
	}
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: msg}
}

func Error(code pb.ErrorCode, err error) *pb.RetInfo {
	if err == nil {
		return &pb.RetInfo{Code: code}
	}
	return &pb.RetInfo{Code: code, Msg: err.Error()}
}

func StringValue(name, value string) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}},
	}
}

func DoubleValue(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func IntValue(name string, value int64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_INT,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: value}},
	}
}

func (s *Store) WriteRows(ctx context.Context, rows []*pb.DataRow, mode pb.WriteMode) error {
	store, err := s.factStore()
	if err != nil {
		return err
	}
	return store.WriteRows(ctx, rows, mode)
}

func (s *Store) ReadRows(ctx context.Context, scope *pb.DataScope, readMode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, rowIDs []string, columnNames []string, page *pb.Page) ([]*pb.DataRow, *pb.PageResult, error) {
	store, err := s.factStore()
	if err != nil {
		return nil, nil, err
	}
	return store.ReadRows(ctx, scope, readMode, timeRange, snapshotTime, rowIDs, columnNames, page)
}

func (s *Store) factStore() (*devicepebble.Store, error) {
	path := filepath.Join(s.root, "pebble", "main")
	if value, ok := pebbleStores.Load(path); ok {
		return value.(*devicepebble.Store), nil
	}
	opened, err := devicepebble.Open(devicepebble.Options{Path: path})
	if err != nil {
		return nil, err
	}
	actual, loaded := pebbleStores.LoadOrStore(path, opened)
	if loaded {
		_ = opened.Close()
	}
	return actual.(*devicepebble.Store), nil
}
