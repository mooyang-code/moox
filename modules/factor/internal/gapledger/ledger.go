// Package gapledger 是因子引擎的跳过账本：每次因回看窗口空洞、或实时周期里标的还在预热而跳过的 (标的, 周期, 因子)
// 都追加一行到按月的 JSONL 文件，永不自动删除。它同时是永久审计（可用 DuckDB 的 read_json_auto 或 jq 分析空洞的频率、
// 集中的标的与时段、从发现到补齐的耗时；预热跳过的行带 reason=warming）和恢复的待办来源：启动时回放全部账本重建待恢复集合。
// 设计见 docs/计划/因子窗口空洞防护详细设计.md 2.5。
package gapledger

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"trpc.group/trpc-go/trpc-go/log"
)

// 账本事件。
const (
	EventSkip      = "skip"      // 因空洞（reason 为空）或预热中（reason=warming）跳过
	EventRepaired  = "repaired"  // 已补齐并写入
	EventCancelled = "cancelled" // 因子定义已变更或因子、标的已不在范围，不再恢复
	EventGaveUp    = "gave_up"   // 超过期限仍补不上，交给人工
)

// FactorRef 是一行里的一个因子及其定义哈希。
type FactorRef struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}

type line struct {
	Event   string      `json:"event"`
	At      time.Time   `json:"at"`
	Set     string      `json:"set"`
	Subject string      `json:"subject"`
	Period  time.Time   `json:"period"`
	Factors []FactorRef `json:"factors"`
	SkipID  string      `json:"skip_id"`
	Missing []time.Time `json:"missing,omitempty"`
	Tags    []string    `json:"tags,omitempty"`
	Trigger string      `json:"trigger,omitempty"`
	Reason  string      `json:"reason,omitempty"`
}

type key struct {
	set, subject string
	period       int64
	factorID     string
	hash         string
}

// Entry 是一条待恢复项：一个标的、一个周期、一个因子（带定义哈希）。
type Entry struct {
	Set       string
	Subject   string
	Period    time.Time
	FactorID  string
	Hash      string
	SkipID    string
	FirstSeen time.Time
	Trigger   string
	Missing   []time.Time
	Tags      []string
	// Reason 是跳过的原因：空洞为空，预热中为 pipeline.SkipReasonWarming。
	Reason string
}

// Ref 精确指向一条待恢复项：确认时必须带上读取待办时的 SkipID，期间被再次失效（SkipID 已变）的项不会被移除。
type Ref struct {
	Set      string
	Subject  string
	Period   time.Time
	FactorID string
	Hash     string
	SkipID   string
}

func (e Entry) Ref() Ref {
	return Ref{Set: e.Set, Subject: e.Subject, Period: e.Period, FactorID: e.FactorID, Hash: e.Hash, SkipID: e.SkipID}
}

func refKey(ref Ref) key {
	return key{set: ref.Set, subject: ref.Subject, period: ref.Period.UTC().Unix(), factorID: ref.FactorID, hash: ref.Hash}
}

// Ledger 是账本。内存里的待恢复集合始终是权威：写盘失败只记错误并标记降级，不影响本进程内的恢复与判断。
type Ledger struct {
	dir  string
	now  func() time.Time
	mu   sync.Mutex
	file *os.File
	// month 是当前打开文件对应的月份（UTC，YYYYMM）。
	month   string
	pending map[key]*Entry
	// degraded 为 true 表示最近一次写盘失败：账本可能不完整，健康检查据此降级。
	degraded bool
	// skipped 是自启动以来因空洞跳过的条目数；ignored 是回放时无法解析而忽略的行数。
	skipped int64
	ignored int
}

// Reset 删除账本目录里的全部账本文件（factor-gap-*.jsonl），返回删除的文件数；目录不存在视为没有账本。
// 用于数据被整体清空之后：账本里的待恢复项指向已不存在的数据，留着只会让恢复一直白跑、超期后还会告警。
// 调用方必须保证没有引擎进程在使用该目录：引擎持有打开的文件，并在内存里维护待恢复集合。
func Reset(dir string) (int, error) {
	if strings.TrimSpace(dir) == "" {
		return 0, fmt.Errorf("gap ledger directory is required")
	}
	files, err := filepath.Glob(filepath.Join(dir, "factor-gap-*.jsonl"))
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, file := range files {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, fmt.Errorf("remove gap ledger %s: %w", file, err)
		}
		removed++
	}
	return removed, nil
}

// Open 打开账本目录并回放全部账本文件重建待恢复集合；giveUp > 0 时，已超过期限的待恢复项立即追加 gave_up
// 并返回它们（停机期间超期的项不会无声消失，调用方应告警）。
func Open(dir string, now func() time.Time, giveUp time.Duration) (*Ledger, []Entry, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil, fmt.Errorf("gap ledger directory is required")
	}
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, fmt.Errorf("create gap ledger directory: %w", err)
	}
	l := &Ledger{dir: dir, now: now, pending: make(map[key]*Entry)}
	if err := l.replay(); err != nil {
		return nil, nil, err
	}
	var expired []Entry
	if giveUp > 0 {
		var err error
		if expired, err = l.ExpireOverdue(giveUp); err != nil {
			log.Errorf("factor_gap_ledger_expire_failed error=%v", err)
		}
	}
	return l, expired, nil
}

func (l *Ledger) replay() error {
	files, err := filepath.Glob(filepath.Join(l.dir, "factor-gap-*.jsonl"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, path := range files {
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open gap ledger %s: %w", path, err)
		}
		reader := bufio.NewReaderSize(file, 1<<20)
		for {
			raw, readErr := reader.ReadBytes('\n')
			// 末尾没有换行的是崩溃留下的半行，只有以换行结尾的完整行才有效。
			if len(raw) > 0 && raw[len(raw)-1] == '\n' {
				var entry line
				if json.Unmarshal(raw, &entry) != nil {
					l.ignored++
				} else {
					l.apply(entry)
				}
			} else if len(raw) > 0 {
				l.ignored++
			}
			if readErr != nil {
				break
			}
		}
		_ = file.Close()
	}
	return nil
}

// apply 把一行应用到内存里的待恢复集合（回放与实时共用，保证两者一致）。
func (l *Ledger) apply(entry line) {
	for _, factor := range entry.Factors {
		k := key{set: entry.Set, subject: entry.Subject, period: entry.Period.UTC().Unix(), factorID: factor.ID, hash: factor.Hash}
		switch entry.Event {
		case EventSkip:
			if existing := l.pending[k]; existing != nil {
				// 同键重复：skipID 取最后一条（再次失效必须让它变化），发现时间取最早。
				existing.SkipID = entry.SkipID
				if entry.Trigger != "" {
					existing.Trigger = entry.Trigger
				}
				existing.Missing, existing.Tags, existing.Reason = entry.Missing, entry.Tags, entry.Reason
				continue
			}
			l.pending[k] = &Entry{
				Set: entry.Set, Subject: entry.Subject, Period: entry.Period.UTC(), FactorID: factor.ID, Hash: factor.Hash,
				SkipID: entry.SkipID, FirstSeen: entry.At.UTC(), Trigger: entry.Trigger, Missing: entry.Missing, Tags: entry.Tags,
				Reason: entry.Reason,
			}
		case EventRepaired, EventCancelled, EventGaveUp:
			// 只移除 skipID 一致的项：期间再次失效（skipID 已变）的项保留，留待下一轮。
			if existing := l.pending[k]; existing != nil && existing.SkipID == entry.SkipID {
				delete(l.pending, k)
			}
		}
	}
}

// RecordGaps 实现 pipeline.GapSink：每个 (标的, 周期, 原因) 追加一行 skip（带新的 skipID，同一行的多个因子共用），
// 一次写入后 fsync。内存先更新，写盘失败返回错误并标记降级。
func (l *Ledger) RecordGaps(_ context.Context, entries []pipeline.GapEntry) error {
	if len(entries) == 0 {
		return nil
	}
	type groupKey struct {
		set, subject, reason string
		period               int64
	}
	groups := make(map[groupKey]*line)
	var order []groupKey
	now := l.now().UTC()
	for _, entry := range entries {
		gk := groupKey{set: entry.SetID, subject: entry.Subject, reason: entry.Reason, period: entry.Period.UTC().Unix()}
		row := groups[gk]
		if row == nil {
			skipID, err := newSkipID()
			if err != nil {
				return err
			}
			row = &line{Event: EventSkip, At: now, Set: entry.SetID, Subject: entry.Subject, Period: entry.Period.UTC(), SkipID: skipID, Trigger: entry.TriggerEventID, Reason: entry.Reason}
			groups[gk] = row
			order = append(order, gk)
		}
		row.Factors = append(row.Factors, FactorRef{ID: entry.FactorID, Hash: entry.DefinitionHash})
		row.Missing = mergeTimes(row.Missing, entry.Missing)
		row.Tags = mergeStrings(row.Tags, entry.Tags)
	}
	lines := make([]line, 0, len(order))
	for _, gk := range order {
		lines = append(lines, *groups[gk])
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, row := range lines {
		l.apply(row)
		if row.Reason == "" {
			l.skipped += int64(len(row.Factors))
		}
	}
	return l.appendLocked(lines)
}

// Pending 返回当前全部待恢复项的快照（按集合、标的、周期、因子排序）。
func (l *Ledger) Pending() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pendingLocked(func(Entry) bool { return true })
}

// PendingFor 返回一个集合下若干标的的待恢复项；恢复在集合锁内用它重读待办，保证用到最新的 skipID。
func (l *Ledger) PendingFor(set string, subjects []string) []Entry {
	wanted := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		wanted[subject] = struct{}{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pendingLocked(func(e Entry) bool {
		_, ok := wanted[e.Subject]
		return ok && e.Set == set
	})
}

func (l *Ledger) pendingLocked(match func(Entry) bool) []Entry {
	out := make([]Entry, 0, len(l.pending))
	for _, entry := range l.pending {
		if match(*entry) {
			copied := *entry
			copied.Missing = append([]time.Time(nil), entry.Missing...)
			copied.Tags = append([]string(nil), entry.Tags...)
			out = append(out, copied)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Set != b.Set:
			return a.Set < b.Set
		case a.Subject != b.Subject:
			return a.Subject < b.Subject
		case !a.Period.Equal(b.Period):
			return a.Period.Before(b.Period)
		default:
			return a.FactorID < b.FactorID
		}
	})
	return out
}

// HasPendingIn 报告一个集合的某个周期是否还有待恢复项；补算据此判断这一期是否已经补齐。
func (l *Ledger) HasPendingIn(set string, period time.Time) bool {
	at := period.UTC().Unix()
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := range l.pending {
		if k.set == set && k.period == at {
			return true
		}
	}
	return false
}

// PendingCount 是待恢复项的数量（指标用）。
func (l *Ledger) PendingCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.pending)
}

// Degraded 报告最近一次写盘是否失败。
func (l *Ledger) Degraded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.degraded
}

// SkippedTotal 是自启动以来因空洞跳过的条目数（指标用）；预热跳过不计入。
func (l *Ledger) SkippedTotal() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.skipped
}

// Ignored 是回放时忽略的无法解析的行数。
func (l *Ledger) Ignored() int { return l.ignored }

// MarkRepaired 追加 repaired 并从待恢复集合移除（只移除 skipID 一致的项）。
func (l *Ledger) MarkRepaired(refs []Ref) error { return l.terminate(EventRepaired, "", refs) }

// MarkCancelled 追加 cancelled。
func (l *Ledger) MarkCancelled(refs []Ref, reason string) error {
	return l.terminate(EventCancelled, reason, refs)
}

// MarkGaveUp 追加 gave_up。
func (l *Ledger) MarkGaveUp(refs []Ref, reason string) error {
	return l.terminate(EventGaveUp, reason, refs)
}

func (l *Ledger) terminate(event, reason string, refs []Ref) error {
	if len(refs) == 0 {
		return nil
	}
	type groupKey struct {
		set, subject, skipID string
		period               int64
	}
	groups := make(map[groupKey]*line)
	var order []groupKey
	now := l.now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ref := range refs {
		existing := l.pending[refKey(ref)]
		if existing == nil || existing.SkipID != ref.SkipID {
			continue // 已被终结，或期间再次失效：不重复写，也不移除新的待办
		}
		gk := groupKey{set: ref.Set, subject: ref.Subject, skipID: ref.SkipID, period: ref.Period.UTC().Unix()}
		row := groups[gk]
		if row == nil {
			row = &line{Event: event, At: now, Set: ref.Set, Subject: ref.Subject, Period: ref.Period.UTC(), SkipID: ref.SkipID, Reason: reason}
			groups[gk] = row
			order = append(order, gk)
		}
		row.Factors = append(row.Factors, FactorRef{ID: ref.FactorID, Hash: ref.Hash})
	}
	lines := make([]line, 0, len(order))
	for _, gk := range order {
		lines = append(lines, *groups[gk])
	}
	for _, row := range lines {
		l.apply(row)
	}
	return l.appendLocked(lines)
}

// ExpireOverdue 对发现时间已超过 giveUp 的待恢复项追加 gave_up 并返回它们（调用方告警）。
func (l *Ledger) ExpireOverdue(giveUp time.Duration) ([]Entry, error) {
	if giveUp <= 0 {
		return nil, nil
	}
	cutoff := l.now().UTC().Add(-giveUp)
	var overdue []Entry
	var refs []Ref
	for _, entry := range l.Pending() {
		if entry.FirstSeen.Before(cutoff) {
			overdue = append(overdue, entry)
			refs = append(refs, entry.Ref())
		}
	}
	return overdue, l.MarkGaveUp(refs, "timeout")
}

// appendLocked 追加若干行并 fsync；调用方持有 l.mu。写盘失败标记降级但不回滚内存。
func (l *Ledger) appendLocked(lines []line) error {
	if len(lines) == 0 {
		return nil
	}
	file, err := l.openLocked()
	if err != nil {
		l.degraded = true
		return err
	}
	var batch []byte
	for _, row := range lines {
		raw, err := json.Marshal(row)
		if err != nil {
			l.degraded = true
			return err
		}
		batch = append(batch, raw...)
		batch = append(batch, '\n')
	}
	if _, err := file.Write(batch); err != nil {
		l.degraded = true
		l.closeLocked()
		return fmt.Errorf("append gap ledger: %w", err)
	}
	if err := file.Sync(); err != nil {
		l.degraded = true
		return fmt.Errorf("sync gap ledger: %w", err)
	}
	l.degraded = false
	return nil
}

func (l *Ledger) openLocked() (*os.File, error) {
	month := l.now().UTC().Format("200601")
	if l.file != nil && l.month == month {
		return l.file, nil
	}
	l.closeLocked()
	path := filepath.Join(l.dir, "factor-gap-"+month+".jsonl")
	needNewline := false
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		if f, err := os.Open(path); err == nil {
			buf := make([]byte, 1)
			if _, err := f.ReadAt(buf, info.Size()-1); err == nil && buf[0] != '\n' {
				needNewline = true // 崩溃留下的半行：先补一个换行，新行不会接在它后面
			}
			_ = f.Close()
		}
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open gap ledger: %w", err)
	}
	if needNewline {
		if _, err := file.Write([]byte("\n")); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	l.file, l.month = file, month
	return file, nil
}

func (l *Ledger) closeLocked() {
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

// Close 关闭账本文件。
func (l *Ledger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeLocked()
	return nil
}

func newSkipID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate skip id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func mergeTimes(values []time.Time, more []time.Time) []time.Time {
	for _, candidate := range more {
		found := false
		for _, existing := range values {
			if existing.Equal(candidate) {
				found = true
				break
			}
		}
		if !found {
			values = append(values, candidate.UTC())
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Before(values[j]) })
	return values
}

func mergeStrings(values []string, more []string) []string {
	for _, candidate := range more {
		found := false
		for _, existing := range values {
			if existing == candidate {
				found = true
				break
			}
		}
		if !found {
			values = append(values, candidate)
		}
	}
	sort.Strings(values)
	return values
}
