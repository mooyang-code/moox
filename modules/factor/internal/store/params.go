package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
)

// 参数请求与冻结（因子动态调参执行计划 2.4、2.6）。提交与冻结在同一个库里串行化：冻结时在一个事务里为每个成员
// 选出该期库内序号最大的已接受请求（没有就沿用上一期的冻结快照，从未有过就用默认值），写冻结区间并推进集合的
// 已冻结期次；之后到达的同期请求一律 too_late。

type paramRequestRow struct {
	Seq           int64     `gorm:"column:c_seq"`
	RequestID     string    `gorm:"column:c_request_id"`
	SetID         string    `gorm:"column:c_set_id"`
	MemberAlias   string    `gorm:"column:c_member_alias"`
	PeriodTime    int64     `gorm:"column:c_period_time"`
	BasedOnPeriod int64     `gorm:"column:c_based_on_period"`
	SnapshotHash  string    `gorm:"column:c_snapshot_hash"`
	Writer        string    `gorm:"column:c_writer"`
	Status        string    `gorm:"column:c_status"`
	CreatedAt     time.Time `gorm:"column:c_ctime"`
	SnapshotJSON  string    `gorm:"column:c_snapshot_json"`
}

const paramRequestColumns = `r.c_seq, r.c_request_id, r.c_set_id, r.c_member_alias, r.c_period_time, r.c_based_on_period,
	r.c_snapshot_hash, r.c_writer, r.c_status, r.c_ctime, COALESCE(p.c_snapshot_json, '') AS c_snapshot_json`

func (r paramRequestRow) domain() (domain.ParamRequest, error) {
	snapshot, err := domain.ParseParamSnapshot(r.SnapshotJSON)
	if err != nil {
		return domain.ParamRequest{}, err
	}
	request := domain.ParamRequest{
		Seq: r.Seq, RequestID: r.RequestID, SetID: r.SetID, MemberAlias: r.MemberAlias,
		PeriodTime: time.Unix(r.PeriodTime, 0).UTC(), Snapshot: snapshot, SnapshotHash: r.SnapshotHash,
		Writer: r.Writer, Status: r.Status, CreatedAt: r.CreatedAt,
	}
	if r.BasedOnPeriod != 0 {
		request.BasedOnPeriod = time.Unix(r.BasedOnPeriod, 0).UTC()
	}
	return request, nil
}

type paramIntervalRow struct {
	SetID        string `gorm:"column:c_set_id"`
	MemberAlias  string `gorm:"column:c_member_alias"`
	StartPeriod  int64  `gorm:"column:c_start_period"`
	EndPeriod    int64  `gorm:"column:c_end_period"`
	SnapshotHash string `gorm:"column:c_snapshot_hash"`
	Source       string `gorm:"column:c_source"`
	SnapshotJSON string `gorm:"column:c_snapshot_json"`
}

const paramIntervalColumns = `f.c_set_id, f.c_member_alias, f.c_start_period, f.c_end_period, f.c_snapshot_hash, f.c_source,
	p.c_snapshot_json`

func (r paramIntervalRow) domain() (domain.ParamInterval, error) {
	snapshot, err := domain.ParseParamSnapshot(r.SnapshotJSON)
	if err != nil {
		return domain.ParamInterval{}, err
	}
	return domain.ParamInterval{
		SetID: r.SetID, MemberAlias: r.MemberAlias, Start: time.Unix(r.StartPeriod, 0).UTC(), End: time.Unix(r.EndPeriod, 0).UTC(),
		Snapshot: snapshot, SnapshotHash: r.SnapshotHash, Source: r.Source,
	}, nil
}

// SubmitParamRequest 接受一份参数请求。同一 (request_id, 集合, 成员) 重复提交返回已存的请求；
// 期次不晚于集合已冻结的期次时返回 domain.ErrParamsTooLate。快照的合法性由调用方先检查。
func (s *Store) SubmitParamRequest(ctx context.Context, request domain.ParamRequest) (domain.ParamRequest, error) {
	if s == nil || s.db == nil {
		return domain.ParamRequest{}, fmt.Errorf("factor database is not open")
	}
	request.RequestID, request.SetID, request.MemberAlias = strings.TrimSpace(request.RequestID), strings.TrimSpace(request.SetID),
		strings.TrimSpace(request.MemberAlias)
	if request.RequestID == "" || request.SetID == "" || request.MemberAlias == "" || request.PeriodTime.IsZero() {
		return domain.ParamRequest{}, fmt.Errorf("request_id, set_id, member and period_time are required")
	}
	request.SnapshotHash = request.Snapshot.Hash()
	var stored domain.ParamRequest
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing, found, err := findParamRequest(tx, request.RequestID, request.SetID, request.MemberAlias)
		if err != nil {
			return err
		}
		if found {
			if existing.SnapshotHash != request.SnapshotHash || !existing.PeriodTime.Equal(request.PeriodTime.UTC()) {
				return fmt.Errorf("%w: request %q was already submitted with different content", ErrConflict, request.RequestID)
			}
			stored = existing
			return nil
		}
		frozen, ok, err := frozenThrough(tx, request.SetID)
		if err != nil {
			return err
		}
		if ok && !request.PeriodTime.After(frozen) {
			return fmt.Errorf("%w（已冻结到 %s）", domain.ErrParamsTooLate, frozen.Format(time.RFC3339))
		}
		if err := putSnapshot(tx, request.Snapshot); err != nil {
			return err
		}
		var basedOn int64
		if !request.BasedOnPeriod.IsZero() {
			basedOn = request.BasedOnPeriod.Unix()
		}
		if err := tx.Exec(`
			INSERT INTO t_factor_param_requests
			(c_request_id, c_set_id, c_member_alias, c_period_time, c_based_on_period, c_snapshot_hash, c_writer, c_status, c_ctime)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, request.RequestID, request.SetID, request.MemberAlias, request.PeriodTime.Unix(), basedOn, request.SnapshotHash,
			request.Writer, domain.ParamRequestAccepted, time.Now().UTC()).Error; err != nil {
			return err
		}
		stored, _, err = findParamRequest(tx, request.RequestID, request.SetID, request.MemberAlias)
		return err
	})
	return stored, err
}

func findParamRequest(tx *gorm.DB, requestID, setID, alias string) (domain.ParamRequest, bool, error) {
	var row paramRequestRow
	result := tx.Raw(`SELECT `+paramRequestColumns+` FROM t_factor_param_requests r
		LEFT JOIN t_factor_param_snapshots p ON p.c_snapshot_hash = r.c_snapshot_hash
		WHERE r.c_request_id = ? AND r.c_set_id = ? AND r.c_member_alias = ?`, requestID, setID, alias).Scan(&row)
	if result.Error != nil || result.RowsAffected == 0 {
		return domain.ParamRequest{}, false, result.Error
	}
	request, err := row.domain()
	return request, err == nil, err
}

func frozenThrough(tx *gorm.DB, setID string) (time.Time, bool, error) {
	var through []int64
	if err := tx.Raw(`SELECT c_frozen_through FROM t_factor_param_frozen_periods WHERE c_set_id = ?`, setID).Scan(&through).Error; err != nil {
		return time.Time{}, false, err
	}
	if len(through) == 0 {
		return time.Time{}, false, nil
	}
	return time.Unix(through[0], 0).UTC(), true, nil
}

// FrozenThrough 返回集合已冻结到的最新一期。
func (s *Store) FrozenThrough(ctx context.Context, setID string) (time.Time, bool, error) {
	if s == nil || s.db == nil {
		return time.Time{}, false, fmt.Errorf("factor database is not open")
	}
	return frozenThrough(s.db.WithContext(ctx), strings.TrimSpace(setID))
}

func putSnapshot(tx *gorm.DB, snapshot domain.ParamSnapshot) error {
	return tx.Exec(`INSERT OR IGNORE INTO t_factor_param_snapshots (c_snapshot_hash, c_snapshot_json, c_ctime) VALUES (?, ?, ?)`,
		snapshot.Hash(), snapshot.Canonical(), time.Now().UTC()).Error
}

// FreezeMember 是冻结一个成员时需要的信息：别名与它的默认快照（从未有过冻结记录时使用）。
type FreezeMember struct {
	Alias    string
	Defaults domain.ParamSnapshot
}

// FreezeParams 冻结集合在 period 这一期的参数并返回每个成员的冻结结果；previous 是网格上的上一期，用于判断能否
// 延长上一个区间。已冻结过的成员期次原样返回（引擎重试、重投、重启后重算都拿到同一份），所以调用是幂等的。
func (s *Store) FreezeParams(ctx context.Context, setID string, period, previous time.Time, members []FreezeMember) ([]domain.ParamFreeze, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	setID = strings.TrimSpace(setID)
	if setID == "" || period.IsZero() || !previous.Before(period) {
		return nil, fmt.Errorf("set id, period and an earlier previous period are required")
	}
	period, previous = period.UTC(), previous.UTC()
	var freezes []domain.ParamFreeze
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		frozen, hasFrozen, err := frozenThrough(tx, setID)
		if err != nil {
			return err
		}
		fresh := !hasFrozen || period.After(frozen)
		for _, member := range members {
			freeze, err := freezeMember(tx, setID, member, period, previous, fresh)
			if err != nil {
				return fmt.Errorf("冻结成员 %s：%w", member.Alias, err)
			}
			freezes = append(freezes, freeze)
		}
		if fresh {
			return tx.Exec(`
				INSERT INTO t_factor_param_frozen_periods (c_set_id, c_frozen_through, c_mtime) VALUES (?, ?, ?)
				ON CONFLICT (c_set_id) DO UPDATE SET c_frozen_through = excluded.c_frozen_through, c_mtime = excluded.c_mtime
			`, setID, period.Unix(), time.Now().UTC()).Error
		}
		return nil
	})
	return freezes, err
}

func freezeMember(tx *gorm.DB, setID string, member FreezeMember, period, previous time.Time, fresh bool) (domain.ParamFreeze, error) {
	covering, found, err := intervalCovering(tx, setID, member.Alias, period)
	if err != nil {
		return domain.ParamFreeze{}, err
	}
	if found {
		freeze := domain.ParamFreeze{SetID: setID, MemberAlias: member.Alias, PeriodTime: period, Snapshot: covering.Snapshot,
			SnapshotHash: covering.SnapshotHash, Source: covering.Source}
		if covering.Source == domain.ParamsRequested {
			freeze.RequestID, _ = appliedRequestID(tx, setID, member.Alias, period)
		}
		return freeze, nil
	}
	freeze := domain.ParamFreeze{SetID: setID, MemberAlias: member.Alias, PeriodTime: period}
	var chosen *domain.ParamRequest
	if fresh {
		if chosen, err = latestAcceptedRequest(tx, setID, member.Alias, period); err != nil {
			return domain.ParamFreeze{}, err
		}
	}
	switch {
	case chosen != nil:
		freeze.Snapshot, freeze.Source, freeze.RequestID = chosen.Snapshot, domain.ParamsRequested, chosen.RequestID
	default:
		last, ok, err := lastIntervalBefore(tx, setID, member.Alias, period)
		if err != nil {
			return domain.ParamFreeze{}, err
		}
		// 成员的参数声明在两期之间变过（增删参数），旧快照缺参数或多参数，不能再沿用：改回当前声明的默认值。
		if ok && !sameParamNames(last.Snapshot, member.Defaults) {
			ok = false
		}
		if ok {
			// 从未提交过时沿用的仍是默认值，来源记为 default（连续沿用的告警只看真正提交过之后的沿用）。
			freeze.Snapshot, freeze.Source = last.Snapshot, domain.ParamsCarried
			if last.Source == domain.ParamsDefault {
				freeze.Source = domain.ParamsDefault
			}
		} else {
			freeze.Snapshot, freeze.Source = member.Defaults, domain.ParamsDefault
		}
	}
	freeze.SnapshotHash = freeze.Snapshot.Hash()
	if err := writeInterval(tx, setID, member.Alias, period, previous, freeze.Snapshot, freeze.Source); err != nil {
		return domain.ParamFreeze{}, err
	}
	if chosen != nil {
		if err := tx.Exec(`UPDATE t_factor_param_requests SET c_status = CASE WHEN c_seq = ? THEN ? ELSE ? END
			WHERE c_set_id = ? AND c_member_alias = ? AND c_period_time = ? AND c_status = ?`,
			chosen.Seq, domain.ParamRequestApplied, domain.ParamRequestSuperseded, setID, member.Alias, period.Unix(),
			domain.ParamRequestAccepted).Error; err != nil {
			return domain.ParamFreeze{}, err
		}
	}
	return freeze, nil
}

// sameParamNames 判断两份快照的参数名集合是否一致。
func sameParamNames(a, b domain.ParamSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for name := range a {
		if _, ok := b[name]; !ok {
			return false
		}
	}
	return true
}

func latestAcceptedRequest(tx *gorm.DB, setID, alias string, period time.Time) (*domain.ParamRequest, error) {
	var row paramRequestRow
	result := tx.Raw(`SELECT `+paramRequestColumns+` FROM t_factor_param_requests r
		LEFT JOIN t_factor_param_snapshots p ON p.c_snapshot_hash = r.c_snapshot_hash
		WHERE r.c_set_id = ? AND r.c_member_alias = ? AND r.c_period_time = ? AND r.c_status = ?
		ORDER BY r.c_seq DESC LIMIT 1`, setID, alias, period.Unix(), domain.ParamRequestAccepted).Scan(&row)
	if result.Error != nil || result.RowsAffected == 0 {
		return nil, result.Error
	}
	request, err := row.domain()
	if err != nil {
		return nil, err
	}
	return &request, nil
}

func appliedRequestID(tx *gorm.DB, setID, alias string, period time.Time) (string, error) {
	var ids []string
	err := tx.Raw(`SELECT c_request_id FROM t_factor_param_requests
		WHERE c_set_id = ? AND c_member_alias = ? AND c_period_time = ? AND c_status = ? LIMIT 1`,
		setID, alias, period.Unix(), domain.ParamRequestApplied).Scan(&ids).Error
	if err != nil || len(ids) == 0 {
		return "", err
	}
	return ids[0], nil
}

func intervalCovering(tx *gorm.DB, setID, alias string, period time.Time) (domain.ParamInterval, bool, error) {
	var row paramIntervalRow
	result := tx.Raw(`SELECT `+paramIntervalColumns+` FROM t_factor_param_freezes f
		JOIN t_factor_param_snapshots p ON p.c_snapshot_hash = f.c_snapshot_hash
		WHERE f.c_set_id = ? AND f.c_member_alias = ? AND f.c_start_period <= ? AND f.c_end_period >= ?
		ORDER BY f.c_start_period DESC LIMIT 1`, setID, alias, period.Unix(), period.Unix()).Scan(&row)
	if result.Error != nil || result.RowsAffected == 0 {
		return domain.ParamInterval{}, false, result.Error
	}
	interval, err := row.domain()
	return interval, err == nil, err
}

func lastIntervalBefore(tx *gorm.DB, setID, alias string, period time.Time) (domain.ParamInterval, bool, error) {
	var row paramIntervalRow
	result := tx.Raw(`SELECT `+paramIntervalColumns+` FROM t_factor_param_freezes f
		JOIN t_factor_param_snapshots p ON p.c_snapshot_hash = f.c_snapshot_hash
		WHERE f.c_set_id = ? AND f.c_member_alias = ? AND f.c_end_period < ?
		ORDER BY f.c_end_period DESC LIMIT 1`, setID, alias, period.Unix()).Scan(&row)
	if result.Error != nil || result.RowsAffected == 0 {
		return domain.ParamInterval{}, false, result.Error
	}
	interval, err := row.domain()
	return interval, err == nil, err
}

// writeInterval 记录成员 period 这一期的冻结：上一个区间恰好结束在 previous、快照与来源都相同时延长它，否则新开一个区间。
func writeInterval(tx *gorm.DB, setID, alias string, period, previous time.Time, snapshot domain.ParamSnapshot, source string) error {
	if err := putSnapshot(tx, snapshot); err != nil {
		return err
	}
	hash := snapshot.Hash()
	result := tx.Exec(`UPDATE t_factor_param_freezes SET c_end_period = ?, c_mtime = ?
		WHERE c_set_id = ? AND c_member_alias = ? AND c_end_period = ? AND c_snapshot_hash = ? AND c_source = ?`,
		period.Unix(), time.Now().UTC(), setID, alias, previous.Unix(), hash, source)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	return tx.Exec(`INSERT INTO t_factor_param_freezes
		(c_set_id, c_member_alias, c_start_period, c_end_period, c_snapshot_hash, c_source, c_mtime) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		setID, alias, period.Unix(), period.Unix(), hash, source, time.Now().UTC()).Error
}

// ApplyOfflineFreezes 合并引擎在 FactorMgr 不可达时本地冻结的期次。管理端已有记录的期次以管理端为准，快照不同的
// 作为冲突返回（引擎据此重算那一期）；没有记录的写入来源为 carried_offline 的区间，并把那一期已接受的请求标为未生效。
func (s *Store) ApplyOfflineFreezes(ctx context.Context, freezes []domain.ParamFreeze, previous func(time.Time) time.Time) ([]domain.ParamFreeze, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	var conflicts []domain.ParamFreeze
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, freeze := range freezes {
			period := freeze.PeriodTime.UTC()
			covering, found, err := intervalCovering(tx, freeze.SetID, freeze.MemberAlias, period)
			if err != nil {
				return err
			}
			if found {
				if covering.SnapshotHash != freeze.Snapshot.Hash() {
					conflicts = append(conflicts, domain.ParamFreeze{SetID: freeze.SetID, MemberAlias: freeze.MemberAlias, PeriodTime: period,
						Snapshot: covering.Snapshot, SnapshotHash: covering.SnapshotHash, Source: covering.Source})
				}
				continue
			}
			if err := writeInterval(tx, freeze.SetID, freeze.MemberAlias, period, previous(period), freeze.Snapshot,
				domain.ParamsCarriedOffline); err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE t_factor_param_requests SET c_status = ?
				WHERE c_set_id = ? AND c_member_alias = ? AND c_period_time = ? AND c_status = ?`,
				domain.ParamRequestNotApplied, freeze.SetID, freeze.MemberAlias, period.Unix(), domain.ParamRequestAccepted).Error; err != nil {
				return err
			}
			if err := tx.Exec(`
				INSERT INTO t_factor_param_frozen_periods (c_set_id, c_frozen_through, c_mtime) VALUES (?, ?, ?)
				ON CONFLICT (c_set_id) DO UPDATE SET c_frozen_through = MAX(c_frozen_through, excluded.c_frozen_through), c_mtime = excluded.c_mtime
			`, freeze.SetID, period.Unix(), time.Now().UTC()).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return conflicts, err
}

// ListParamIntervals 返回集合（可限定成员）与 [from, to] 相交的冻结区间，外加每个成员结束在 from 之前的最近一个区间
// （from 没有冻结记录时按它沿用），按成员、起点排序。
func (s *Store) ListParamIntervals(ctx context.Context, setID, alias string, from, to time.Time) ([]domain.ParamInterval, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	query := `SELECT ` + paramIntervalColumns + ` FROM t_factor_param_freezes f
		JOIN t_factor_param_snapshots p ON p.c_snapshot_hash = f.c_snapshot_hash
		WHERE f.c_set_id = ? AND ((f.c_end_period >= ? AND f.c_start_period <= ?) OR f.c_end_period = (
			SELECT MAX(g.c_end_period) FROM t_factor_param_freezes g
			WHERE g.c_set_id = f.c_set_id AND g.c_member_alias = f.c_member_alias AND g.c_end_period < ?))`
	args := []any{strings.TrimSpace(setID), from.Unix(), to.Unix(), from.Unix()}
	if alias = strings.TrimSpace(alias); alias != "" {
		query += " AND f.c_member_alias = ?"
		args = append(args, alias)
	}
	query += " ORDER BY f.c_member_alias, f.c_start_period"
	var rows []paramIntervalRow
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	intervals := make([]domain.ParamInterval, 0, len(rows))
	for _, row := range rows {
		interval, err := row.domain()
		if err != nil {
			return nil, err
		}
		intervals = append(intervals, interval)
	}
	return intervals, nil
}

// ListParamRequests 返回集合（可限定成员）在 [from, to] 期次内的请求，按期次、序号排序，最多 limit 条。
func (s *Store) ListParamRequests(ctx context.Context, setID, alias string, from, to time.Time, limit int) ([]domain.ParamRequest, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	query := `SELECT ` + paramRequestColumns + ` FROM t_factor_param_requests r
		LEFT JOIN t_factor_param_snapshots p ON p.c_snapshot_hash = r.c_snapshot_hash
		WHERE r.c_set_id = ? AND r.c_period_time >= ? AND r.c_period_time <= ?`
	args := []any{strings.TrimSpace(setID), from.Unix(), to.Unix()}
	if alias = strings.TrimSpace(alias); alias != "" {
		query += " AND r.c_member_alias = ?"
		args = append(args, alias)
	}
	query += " ORDER BY r.c_period_time, r.c_seq LIMIT ?"
	args = append(args, limit)
	var rows []paramRequestRow
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	requests := make([]domain.ParamRequest, 0, len(rows))
	for _, row := range rows {
		request, err := row.domain()
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, nil
}

// CleanupParams 删除 requestsBefore 之前提交的请求、集合 setID 结束在 freezesBefore 之前的冻结区间，以及不再被引用的快照。
// freezesBefore 为零值时不删冻结区间。
func (s *Store) CleanupParams(ctx context.Context, requestsBefore time.Time, freezesBefore map[string]time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("factor database is not open")
	}
	var removed int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`DELETE FROM t_factor_param_requests WHERE c_ctime < ?`, requestsBefore.UTC())
		if result.Error != nil {
			return result.Error
		}
		removed += result.RowsAffected
		for setID, before := range freezesBefore {
			if before.IsZero() {
				continue
			}
			// 每个成员的最后一个区间始终保留：长期停用或引擎停摆后恢复，沿用的基线不能丢。
			result := tx.Exec(`DELETE FROM t_factor_param_freezes WHERE c_set_id = ? AND c_end_period < ?
				AND c_end_period < (SELECT MAX(f2.c_end_period) FROM t_factor_param_freezes f2
					WHERE f2.c_set_id = t_factor_param_freezes.c_set_id AND f2.c_member_alias = t_factor_param_freezes.c_member_alias)`,
				setID, before.Unix())
			if result.Error != nil {
				return result.Error
			}
			removed += result.RowsAffected
		}
		result = tx.Exec(`DELETE FROM t_factor_param_snapshots WHERE
			c_snapshot_hash NOT IN (SELECT c_snapshot_hash FROM t_factor_param_freezes)
			AND c_snapshot_hash NOT IN (SELECT c_snapshot_hash FROM t_factor_param_requests)`)
		if result.Error != nil {
			return result.Error
		}
		removed += result.RowsAffected
		return nil
	})
	return removed, err
}

// DeleteSetParams 删除集合的全部参数记录（集合删除时）。
func (s *Store) DeleteSetParams(ctx context.Context, setID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, table := range []string{"t_factor_param_requests", "t_factor_param_freezes", "t_factor_param_frozen_periods"} {
			if err := tx.Exec("DELETE FROM "+table+" WHERE c_set_id = ?", setID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// IsTooLate 报告错误是否表示期次已冻结。
func IsTooLate(err error) bool { return errors.Is(err, domain.ErrParamsTooLate) }
