package sqlite

import (
	"context"
	"errors"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// rowScanner 抽象 sql.Row 和 sql.Rows 的扫描能力。

func (s *Store) UpsertDevice(ctx context.Context, item *pb.Device) (*pb.Device, error) {
	if item == nil || item.GetDeviceId() == "" || item.GetName() == "" || item.GetEngine() == "" {
		return nil, errors.New("device_id, name and engine are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_storage_devices (c_device_id, c_name, c_engine, c_endpoint, c_config_json, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_device_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_engine = excluded.c_engine,
			c_endpoint = excluded.c_endpoint,
			c_config_json = excluded.c_config_json,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetDeviceId(), item.GetName(), item.GetEngine(), item.GetEndpoint(), defaultJSON(item.GetConfigJson()), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetDevice(ctx, item.GetDeviceId())
}

func (s *Store) GetDevice(ctx context.Context, deviceID string) (*pb.Device, error) {
	return getMessage(ctx, s.queryDB(ctx), `SELECT c_attrs_json FROM t_storage_devices WHERE c_device_id = ?`, []any{deviceID}, func() *pb.Device { return &pb.Device{} })
}

func (s *Store) ListDevices(ctx context.Context, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	const where = `
		FROM t_storage_devices
		WHERE (? = '' OR c_engine = ?)`
	args := []any{engine, engine}
	return queryPagedMessages(ctx, s.queryDB(ctx),
		`SELECT c_attrs_json `+where+` ORDER BY c_device_id`,
		`SELECT COUNT(1) `+where,
		args, page, func() *pb.Device { return &pb.Device{} },
	)
}

func (s *Store) RegisterArchiveFile(ctx context.Context, item *pb.ArchiveFile) (*pb.ArchiveFile, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetArchiveFileId() == "" || item.GetDatasetId() == "" || item.GetDeviceId() == "" || item.GetFileUri() == "" {
		return nil, errors.New("space_id, archive_file_id, dataset_id, device_id and file_uri are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	if item.FileFormat == "" {
		item.FileFormat = "parquet"
	}
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	columns, err := marshalJSON(item.GetColumns())
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_archive_files (c_space_id, c_archive_file_id, c_dataset_id, c_device_id, c_partition_key, c_file_uri, c_file_format, c_min_time, c_max_time, c_row_count, c_columns_json, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_archive_file_id) DO UPDATE SET
			c_dataset_id = excluded.c_dataset_id,
			c_device_id = excluded.c_device_id,
			c_partition_key = excluded.c_partition_key,
			c_file_uri = excluded.c_file_uri,
			c_file_format = excluded.c_file_format,
			c_min_time = excluded.c_min_time,
			c_max_time = excluded.c_max_time,
			c_row_count = excluded.c_row_count,
			c_columns_json = excluded.c_columns_json,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetArchiveFileId(), item.GetDatasetId(), item.GetDeviceId(), item.GetPartitionKey(), item.GetFileUri(), item.GetFileFormat(), item.GetMinTime(), item.GetMaxTime(), item.GetRowCount(), columns, item.GetStatus(), raw)
	return item, err
}

func (s *Store) ListArchiveFiles(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error) {
	const where = `
		FROM t_archive_files
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)`
	args := []any{spaceID, spaceID, datasetID, datasetID}
	return queryPagedMessages(ctx, s.queryDB(ctx),
		`SELECT c_attrs_json `+where+` ORDER BY c_partition_key, c_file_uri`,
		`SELECT COUNT(1) `+where,
		args, page, func() *pb.ArchiveFile { return &pb.ArchiveFile{} },
	)
}
