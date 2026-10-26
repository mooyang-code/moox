package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ParamDelivery 是一条等待投递的下一期参数：产出它的结果、实例与会话，以及目标期和各成员的完整快照。
type ParamDelivery struct {
	ResultID   string
	InstanceID string
	SessionID  string
	// Period 是参数生效的目标期（存储起点）。
	Period time.Time
	// Members 是各可变成员的完整快照：{成员键: {参数名: {default, overrides}}}。
	Members json.RawMessage
}

// nextParams 是 c_next_params_json 的结构。
type nextParams struct {
	Period  int64           `json:"period"`
	Members json.RawMessage `json:"members"`
}

// EncodeNextParams 把目标期和各成员的快照编码成结果里的 next_params_json。
func EncodeNextParams(period time.Time, members json.RawMessage) (json.RawMessage, error) {
	if period.IsZero() || len(members) == 0 {
		return nil, errors.New("下一期参数需要目标期和成员快照")
	}
	return json.Marshal(nextParams{Period: period.UTC().Unix(), Members: members})
}

func decodeNextParams(row resultRow) (ParamDelivery, error) {
	var next nextParams
	if err := json.Unmarshal([]byte(row.NextParams), &next); err != nil {
		return ParamDelivery{}, fmt.Errorf("结果 %s 的 next_params_json 无法解析：%w", row.ResultID, err)
	}
	return ParamDelivery{ResultID: row.ResultID, InstanceID: row.InstanceID, SessionID: row.SessionID,
		Period: time.Unix(next.Period, 0).UTC(), Members: next.Members}, nil
}

// PendingParamDeliveries 返回每个启用实例当前会话里待投递的参数（设计 8.3）：只投会话里最新一条带参数的结果，
// 并且它还没送达也没被因子侧明确拒绝；更早的未送达记录被覆盖，不再投递。实例停用或换了会话后，旧会话的记录不再投递。
func (s *Store) PendingParamDeliveries(ctx context.Context, limit int) ([]ParamDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []resultRow
	err := s.db.WithContext(ctx).Raw(`
		SELECT `+qualifiedResultColumns("r")+`
		FROM t_strategy_results r
		JOIN t_strategy_instances i ON i.c_instance_id = r.c_instance_id AND i.c_session_id = r.c_session_id AND i.c_enabled = 1
		WHERE r.c_next_params_json <> '' AND r.c_params_delivered_at IS NULL AND r.c_params_error = ''
		  AND r.c_bar_end_time = (
			SELECT MAX(c_bar_end_time) FROM t_strategy_results
			WHERE c_instance_id = r.c_instance_id AND c_session_id = r.c_session_id AND c_next_params_json <> ''
		  )
		ORDER BY r.c_ctime, r.c_result_id
		LIMIT ?
	`, limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]ParamDelivery, 0, len(rows))
	for _, row := range rows {
		delivery, err := decodeNextParams(row)
		if err != nil {
			return nil, err
		}
		out = append(out, delivery)
	}
	return out, nil
}

// MarkParamsDelivered 记下参数已被因子侧接受（含幂等重复）。
func (s *Store) MarkParamsDelivered(ctx context.Context, resultID string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Exec(`UPDATE t_strategy_results SET c_params_delivered_at = ?, c_params_error = '' WHERE c_result_id = ? AND c_params_delivered_at IS NULL`, now, resultID).Error
}

// MarkParamsRejected 记下参数被因子侧明确拒绝的原因：不再重试，也不算送达（连续没有送达会触发告警）。
func (s *Store) MarkParamsRejected(ctx context.Context, resultID, reason string) error {
	if reason == "" {
		reason = "被因子侧拒绝"
	}
	return s.db.WithContext(ctx).Exec(`UPDATE t_strategy_results SET c_params_error = ? WHERE c_result_id = ? AND c_params_delivered_at IS NULL`, reason, resultID).Error
}

// ParamDeliveryStale 报告实例当前会话里最近 n 条带参数的结果是否都没有送达：不足 n 条时为 false（还谈不上"连续没有送达"）。
func (s *Store) ParamDeliveryStale(ctx context.Context, instanceID, sessionID string, n int) (bool, error) {
	if n <= 0 {
		return false, nil
	}
	var undelivered []int
	err := s.db.WithContext(ctx).Raw(`
		SELECT CASE WHEN c_params_delivered_at IS NULL THEN 1 ELSE 0 END FROM t_strategy_results
		WHERE c_instance_id = ? AND c_session_id = ? AND c_next_params_json <> ''
		ORDER BY c_bar_end_time DESC LIMIT ?
	`, instanceID, sessionID, n).Scan(&undelivered).Error
	if err != nil || len(undelivered) < n {
		return false, err
	}
	for _, flag := range undelivered {
		if flag == 0 {
			return false, nil
		}
	}
	return true, nil
}

// qualifiedResultColumns 给结果表的列名加上表别名前缀。
func qualifiedResultColumns(alias string) string {
	columns := strings.Split(resultColumns, ",")
	for i, column := range columns {
		columns[i] = alias + "." + strings.TrimSpace(column)
	}
	return strings.Join(columns, ", ")
}
