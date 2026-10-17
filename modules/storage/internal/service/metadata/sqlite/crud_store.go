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
