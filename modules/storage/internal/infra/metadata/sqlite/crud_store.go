package sqlite

import (
	"context"
	"errors"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// rowScanner 抽象 sql.Row 和 sql.Rows 的扫描能力。

func (s *Store) UpsertPrimaryStoreNode(ctx context.Context, item *pb.PrimaryStoreNode) (*pb.PrimaryStoreNode, error) {
	if item == nil || item.GetNodeId() == "" || item.GetName() == "" {
		return nil, errors.New("node_id and name are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	if item.Weight == 0 {
		item.Weight = 100
	}
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_primary_store_nodes (c_node_id, c_name, c_endpoint, c_weight, c_status, c_config_json, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_node_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_endpoint = excluded.c_endpoint,
			c_weight = excluded.c_weight,
			c_status = excluded.c_status,
			c_config_json = excluded.c_config_json,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetNodeId(), item.GetName(), item.GetEndpoint(), item.GetWeight(), item.GetStatus(), defaultJSON(item.GetConfigJson()), raw)
	if err != nil {
		return nil, err
	}
	return s.GetPrimaryStoreNode(ctx, item.GetNodeId())
}

func (s *Store) GetPrimaryStoreNode(ctx context.Context, nodeID string) (*pb.PrimaryStoreNode, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_primary_store_nodes WHERE c_node_id = ?`, []any{nodeID}, func() *pb.PrimaryStoreNode { return &pb.PrimaryStoreNode{} })
}

func (s *Store) ListPrimaryStoreNodes(ctx context.Context, page *pb.Page) ([]*pb.PrimaryStoreNode, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `SELECT c_attrs_json FROM t_primary_store_nodes ORDER BY c_node_id`, nil, func() *pb.PrimaryStoreNode { return &pb.PrimaryStoreNode{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) UpsertDevice(ctx context.Context, item *pb.Device) (*pb.Device, error) {
	if item == nil || item.GetDeviceId() == "" || item.GetNodeId() == "" || item.GetName() == "" || item.GetEngine() == "" {
		return nil, errors.New("device_id, node_id, name and engine are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_storage_devices (c_device_id, c_node_id, c_name, c_engine, c_endpoint, c_config_json, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_device_id) DO UPDATE SET
			c_node_id = excluded.c_node_id,
			c_name = excluded.c_name,
			c_engine = excluded.c_engine,
			c_endpoint = excluded.c_endpoint,
			c_config_json = excluded.c_config_json,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetDeviceId(), item.GetNodeId(), item.GetName(), item.GetEngine(), item.GetEndpoint(), defaultJSON(item.GetConfigJson()), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetDevice(ctx, item.GetDeviceId())
}

func (s *Store) GetDevice(ctx context.Context, deviceID string) (*pb.Device, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_storage_devices WHERE c_device_id = ?`, []any{deviceID}, func() *pb.Device { return &pb.Device{} })
}

func (s *Store) ListDevices(ctx context.Context, nodeID string, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_storage_devices
		WHERE (? = '' OR c_node_id = ?)
		  AND (? = '' OR c_engine = ?)
		ORDER BY c_device_id
	`, []any{nodeID, nodeID, engine, engine}, func() *pb.Device { return &pb.Device{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) UpsertPrimaryStoreRoute(ctx context.Context, item *pb.PrimaryStoreRoute) (*pb.PrimaryStoreRoute, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetRouteId() == "" || item.GetDatasetId() == "" || item.GetNodeId() == "" {
		return nil, errors.New("space_id, route_id, dataset_id and node_id are required")
	}
	item.Status = defaultStatus(item.GetStatus())
	if item.Priority == 0 {
		item.Priority = 100
	}
	raw, err := marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_primary_store_routes (c_space_id, c_route_id, c_dataset_id, c_subject_id, c_subject_pattern, c_hash_rule, c_node_id, c_priority, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_route_id) DO UPDATE SET
			c_dataset_id = excluded.c_dataset_id,
			c_subject_id = excluded.c_subject_id,
			c_subject_pattern = excluded.c_subject_pattern,
			c_hash_rule = excluded.c_hash_rule,
			c_node_id = excluded.c_node_id,
			c_priority = excluded.c_priority,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetRouteId(), item.GetDatasetId(), item.GetSubjectId(), item.GetSubjectPattern(), item.GetHashRule(), item.GetNodeId(), item.GetPriority(), item.GetStatus(), raw)
	if err != nil {
		return nil, err
	}
	return s.GetPrimaryStoreRoute(ctx, item.GetSpaceId(), item.GetRouteId())
}

func (s *Store) GetPrimaryStoreRoute(ctx context.Context, spaceID string, routeID string) (*pb.PrimaryStoreRoute, error) {
	return getMessage(ctx, s.db, `SELECT c_attrs_json FROM t_primary_store_routes WHERE c_space_id = ? AND c_route_id = ?`, []any{spaceID, routeID}, func() *pb.PrimaryStoreRoute { return &pb.PrimaryStoreRoute{} })
}

func (s *Store) ListPrimaryStoreRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.PrimaryStoreRoute, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_primary_store_routes
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)
		  AND (? = '' OR c_subject_id = ?)
		  AND (? = '' OR c_node_id = ?)
		ORDER BY c_priority, c_route_id
	`, []any{spaceID, spaceID, datasetID, datasetID, subjectID, subjectID, nodeID, nodeID}, func() *pb.PrimaryStoreRoute { return &pb.PrimaryStoreRoute{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
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
		INSERT INTO t_archive_files (c_space_id, c_archive_file_id, c_dataset_id, c_device_id, c_partition_key, c_file_uri, c_file_format, c_min_time, c_max_time, c_row_count, c_content_hash, c_columns_json, c_status, c_attrs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_archive_file_id) DO UPDATE SET
			c_dataset_id = excluded.c_dataset_id,
			c_device_id = excluded.c_device_id,
			c_partition_key = excluded.c_partition_key,
			c_file_uri = excluded.c_file_uri,
			c_file_format = excluded.c_file_format,
			c_min_time = excluded.c_min_time,
			c_max_time = excluded.c_max_time,
			c_row_count = excluded.c_row_count,
			c_content_hash = excluded.c_content_hash,
			c_columns_json = excluded.c_columns_json,
			c_status = excluded.c_status,
			c_attrs_json = excluded.c_attrs_json
	`, item.GetSpaceId(), item.GetArchiveFileId(), item.GetDatasetId(), item.GetDeviceId(), item.GetPartitionKey(), item.GetFileUri(), item.GetFileFormat(), item.GetMinTime(), item.GetMaxTime(), item.GetRowCount(), item.GetContentHash(), columns, item.GetStatus(), raw)
	return item, err
}

func (s *Store) ListArchiveFiles(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error) {
	items, err := queryMessages(ctx, s.db, `
		SELECT c_attrs_json FROM t_archive_files
		WHERE (? = '' OR c_space_id = ?)
		  AND (? = '' OR c_dataset_id = ?)
		ORDER BY c_partition_key, c_file_uri
	`, []any{spaceID, spaceID, datasetID, datasetID}, func() *pb.ArchiveFile { return &pb.ArchiveFile{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}
