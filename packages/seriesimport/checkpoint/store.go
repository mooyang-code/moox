// Package checkpoint 保存导入任务的断点（SQLite）。
package checkpoint

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/glebarez/go-sqlite"
	"github.com/mooyang-code/moox/packages/seriesimport/schema"
)

// Store 是断点存储。
type Store struct{ db *sql.DB }

// Open 打开（必要时创建）断点库。path 为 ":memory:" 时是内存库（测试用）。
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema.AllSQL()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("初始化断点库：%w", err)
	}
	return &Store{db: db}, nil
}

// Close 关闭断点库。
func (s *Store) Close() error { return s.db.Close() }

// Progress 是一个 (任务, 文件, 目标) 的进度。
type Progress struct {
	BatchesDone int64
	RowsDone    int64
	Status      string
	Error       string
}

// Lookup 返回进度；没有记录时返回零值。
func (s *Store) Lookup(job, file, target string) (Progress, error) {
	var p Progress
	err := s.db.QueryRow(`SELECT c_batches_done, c_rows_done, c_status, c_error FROM t_import_checkpoints WHERE c_job_id = ? AND c_file = ? AND c_target = ?`,
		job, file, target).Scan(&p.BatchesDone, &p.RowsDone, &p.Status, &p.Error)
	if err == sql.ErrNoRows {
		return Progress{}, nil
	}
	return p, err
}

// Store 记录进度（覆盖）。
func (s *Store) Store(job, file, target string, p Progress) error {
	if p.Status == "" {
		p.Status = "running"
	}
	_, err := s.db.Exec(`INSERT INTO t_import_checkpoints (c_job_id, c_file, c_target, c_batches_done, c_rows_done, c_status, c_error, c_mtime)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (c_job_id, c_file, c_target) DO UPDATE SET
    c_batches_done = excluded.c_batches_done, c_rows_done = excluded.c_rows_done,
    c_status = excluded.c_status, c_error = excluded.c_error, c_mtime = excluded.c_mtime`,
		job, file, target, p.BatchesDone, p.RowsDone, p.Status, p.Error, time.Now().UTC().Format(time.RFC3339))
	return err
}

// Entry 是任务的一条进度记录。
type Entry struct {
	File, Target string
	Progress
}

// List 返回任务的全部进度记录。
func (s *Store) List(job string) ([]Entry, error) {
	rows, err := s.db.Query(`SELECT c_file, c_target, c_batches_done, c_rows_done, c_status, c_error FROM t_import_checkpoints WHERE c_job_id = ? ORDER BY c_file, c_target`, job)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.File, &e.Target, &e.BatchesDone, &e.RowsDone, &e.Status, &e.Error); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Jobs 返回有记录的任务 ID。
func (s *Store) Jobs() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT c_job_id FROM t_import_checkpoints ORDER BY c_job_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Get 实现 runner.Checkpoints：已经写完的批数、行数，以及整个文件是否已经写完。
func (s *Store) Get(job, file, target string) (batches, rows int64, done bool, err error) {
	p, err := s.Lookup(job, file, target)
	return p.BatchesDone, p.RowsDone, p.Status == "done", err
}

// Save 实现 runner.Checkpoints：记录写完的批数；done 表示整个文件写完，errText 非空表示失败。
func (s *Store) Save(job, file, target string, batches, rows int64, done bool, errText string) error {
	status := "running"
	switch {
	case done:
		status = "done"
	case errText != "":
		status = "failed"
	}
	return s.Store(job, file, target, Progress{BatchesDone: batches, RowsDone: rows, Status: status, Error: errText})
}
