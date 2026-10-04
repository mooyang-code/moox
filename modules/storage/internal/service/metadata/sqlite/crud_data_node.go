package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func (s *Store) RegisterDataNode(ctx context.Context, nodeID, serviceTarget, initialName string) (*pb.DataNode, error) {
	nodeID = strings.TrimSpace(nodeID)
	serviceTarget = strings.TrimSpace(serviceTarget)
	initialName = strings.TrimSpace(initialName)
	if nodeID == "" || serviceTarget == "" {
		return nil, errors.New("node_id and service_target are required")
	}
	if initialName == "" {
		initialName = nodeID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO t_data_nodes (c_node_id, c_name, c_service_target, c_status)
		VALUES (?, ?, ?, 'active')
		ON CONFLICT(c_node_id) DO UPDATE SET
			c_service_target = excluded.c_service_target,
			c_mtime = CURRENT_TIMESTAMP
	`, nodeID, initialName, serviceTarget)
	if err != nil {
		return nil, err
	}
	return s.GetDataNode(ctx, nodeID)
}

func (s *Store) UpdateDataNode(ctx context.Context, nodeID, name, status string) (*pb.DataNode, error) {
	nodeID = strings.TrimSpace(nodeID)
	name = strings.TrimSpace(name)
	status = strings.TrimSpace(status)
	if nodeID == "" || name == "" {
		return nil, errors.New("node_id and name are required")
	}
	if err := validateDataNodeStatus(status); err != nil {
		return nil, err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE t_data_nodes
		SET c_name = ?, c_status = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_node_id = ?
	`, name, status, nodeID)
	if err != nil {
		return nil, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if affected != 1 {
		return nil, fmt.Errorf("data node %q: %w", nodeID, sql.ErrNoRows)
	}
	return s.GetDataNode(ctx, nodeID)
}

func (s *Store) GetDataNode(ctx context.Context, nodeID string) (*pb.DataNode, error) {
	nodeID = strings.TrimSpace(nodeID)
	return scanDataNode(s.queryDB(ctx).QueryRowContext(ctx, `
		SELECT c_node_id, c_name, c_service_target, c_status, c_ctime, c_mtime
		FROM t_data_nodes WHERE c_node_id = ?
	`, nodeID))
}

func (s *Store) ListDataNodes(ctx context.Context, page *pb.Page) ([]*pb.DataNode, *pb.PageResult, error) {
	pageNo, size, offset := normalizePage(page)
	rows, err := s.queryDB(ctx).QueryContext(ctx, `
		SELECT c_node_id, c_name, c_service_target, c_status, c_ctime, c_mtime
		FROM t_data_nodes ORDER BY c_node_id LIMIT ? OFFSET ?
	`, size, offset)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := make([]*pb.DataNode, 0, size)
	for rows.Next() {
		item, err := scanDataNode(rows)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var total uint32
	if err := s.queryDB(ctx).QueryRowContext(ctx, `SELECT COUNT(1) FROM t_data_nodes`).Scan(&total); err != nil {
		return nil, nil, err
	}
	return items, &pb.PageResult{
		Page: pageNo, Size: size, Total: total,
		HasMore: uint64(offset)+uint64(len(items)) < uint64(total),
	}, nil
}

func (s *Store) DeleteDataNode(ctx context.Context, nodeID string) error {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return errors.New("node_id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT c_status FROM t_data_nodes WHERE c_node_id = ?`, nodeID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("data node %q: %w", nodeID, sql.ErrNoRows)
		}
		return err
	}
	if status != "disabled" {
		return ErrDataNodeMustBeDisabled
	}
	var datasets uint32
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_datasets WHERE c_data_node_id = ?`, nodeID).Scan(&datasets); err != nil {
		return err
	}
	if datasets > 0 {
		return ErrDataNodeReferenced
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM t_data_nodes WHERE c_node_id = ?`, nodeID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return fmt.Errorf("data node %q: %w", nodeID, sql.ErrNoRows)
	}
	return tx.Commit()
}

func validateDataNodeStatus(status string) error {
	if status != "active" && status != "disabled" {
		return fmt.Errorf("data node status must be active or disabled: %q", status)
	}
	return nil
}

func scanDataNode(row rowScanner) (*pb.DataNode, error) {
	var item pb.DataNode
	var createdAt, updatedAt string
	if err := row.Scan(&item.NodeId, &item.Name, &item.ServiceTarget, &item.Status, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("metadata row not found: %w", err)
		}
		return nil, err
	}
	item.CreatedAt = normalizeSQLTimestamp(createdAt)
	item.UpdatedAt = normalizeSQLTimestamp(updatedAt)
	return &item, nil
}
