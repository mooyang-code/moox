package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/udfhost"
	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorudf"
	"gorm.io/gorm"
)

// UdfName 是一个自定义算子名称：ActiveHash 是当前启用的版本，空串表示还没有启用。
type UdfName struct {
	Name       string    `json:"name"`
	ActiveHash string    `json:"active_hash"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// UdfVersion 是自定义算子的一个不可变版本（身份是 wasm 字节的 sha256）。Wasm 只在明确要求时才读出。
type UdfVersion struct {
	Hash       string    `json:"hash"`
	Name       string    `json:"name"`
	Manifest   string    `json:"manifest"` // 包内 manifest.json（语义字段加说明、示例、来源）
	SourceRef  string    `json:"source_ref"`
	SDKVersion string    `json:"sdk_version"`
	Toolchain  string    `json:"toolchain"`
	Report     string    `json:"report"`
	CreatedAt  time.Time `json:"created_at"`
	Wasm       []byte    `json:"-"`
}

type udfVersionRow struct {
	Hash       string    `gorm:"column:c_hash"`
	Name       string    `gorm:"column:c_name"`
	Manifest   string    `gorm:"column:c_manifest"`
	SourceRef  string    `gorm:"column:c_source_ref"`
	SDKVersion string    `gorm:"column:c_sdk_version"`
	Toolchain  string    `gorm:"column:c_toolchain"`
	Report     string    `gorm:"column:c_report"`
	CreatedAt  time.Time `gorm:"column:c_ctime"`
}

const udfVersionColumns = `c_hash, c_name, c_manifest, c_source_ref, c_sdk_version, c_toolchain, c_report, c_ctime`

func (r udfVersionRow) version() UdfVersion {
	return UdfVersion{Hash: r.Hash, Name: r.Name, Manifest: r.Manifest, SourceRef: r.SourceRef, SDKVersion: r.SDKVersion,
		Toolchain: r.Toolchain, Report: r.Report, CreatedAt: r.CreatedAt}
}

// PutUdfVersion 保存一个版本（未启用）：名称不存在时一并创建；同一哈希重复上传只更新说明与示例（c_manifest），
// 其余字段（wasm、来源、报告）不变。created 报告是否新增了版本。
func (s *Store) PutUdfVersion(ctx context.Context, v UdfVersion) (created bool, err error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("factor database is not open")
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT OR IGNORE INTO t_factor_udfs (c_name) VALUES (?)`, v.Name).Error; err != nil {
			return err
		}
		var existing string
		result := tx.Raw(`SELECT c_name FROM t_factor_udf_versions WHERE c_hash = ?`, v.Hash).Scan(&existing)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			if existing != v.Name {
				return fmt.Errorf("%w: version %s already belongs to udf %q", ErrConflict, v.Hash, existing)
			}
			return tx.Exec(`UPDATE t_factor_udf_versions SET c_manifest = ? WHERE c_hash = ?`, v.Manifest, v.Hash).Error
		}
		created = true
		return tx.Exec(`
			INSERT INTO t_factor_udf_versions (c_hash, c_name, c_manifest, c_wasm, c_source_ref, c_sdk_version, c_toolchain, c_report, c_ctime)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, v.Hash, v.Name, v.Manifest, v.Wasm, v.SourceRef, v.SDKVersion, v.Toolchain, v.Report, time.Now().UTC()).Error
	})
	return created, err
}

// GetUdfVersion 返回一个版本；withWasm 为真时同时读出 wasm 字节。
func (s *Store) GetUdfVersion(ctx context.Context, hash string, withWasm bool) (UdfVersion, error) {
	if s == nil || s.db == nil {
		return UdfVersion{}, fmt.Errorf("factor database is not open")
	}
	return getUdfVersion(ctx, s.db, hash, withWasm)
}

func getUdfVersion(ctx context.Context, db *gorm.DB, hash string, withWasm bool) (UdfVersion, error) {
	var row udfVersionRow
	result := db.WithContext(ctx).Raw(`SELECT `+udfVersionColumns+` FROM t_factor_udf_versions WHERE c_hash = ?`, hash).Scan(&row)
	if result.Error != nil {
		return UdfVersion{}, result.Error
	}
	if result.RowsAffected == 0 {
		return UdfVersion{}, gorm.ErrRecordNotFound
	}
	v := row.version()
	if withWasm {
		var blob struct {
			Wasm []byte `gorm:"column:c_wasm"`
		}
		if err := db.WithContext(ctx).Raw(`SELECT c_wasm FROM t_factor_udf_versions WHERE c_hash = ?`, hash).Scan(&blob).Error; err != nil {
			return UdfVersion{}, err
		}
		v.Wasm = blob.Wasm
	}
	return v, nil
}

// GetUdf 返回名称及其全部版本（不含 wasm，按创建时间从新到旧）。
func (s *Store) GetUdf(ctx context.Context, name string) (UdfName, []UdfVersion, error) {
	if s == nil || s.db == nil {
		return UdfName{}, nil, fmt.Errorf("factor database is not open")
	}
	var row struct {
		Name       string    `gorm:"column:c_name"`
		ActiveHash string    `gorm:"column:c_active_hash"`
		CreatedAt  time.Time `gorm:"column:c_ctime"`
		UpdatedAt  time.Time `gorm:"column:c_mtime"`
	}
	result := s.db.WithContext(ctx).Raw(`SELECT c_name, c_active_hash, c_ctime, c_mtime FROM t_factor_udfs WHERE c_name = ?`, name).Scan(&row)
	if result.Error != nil {
		return UdfName{}, nil, result.Error
	}
	if result.RowsAffected == 0 {
		return UdfName{}, nil, gorm.ErrRecordNotFound
	}
	var rows []udfVersionRow
	if err := s.db.WithContext(ctx).Raw(`SELECT `+udfVersionColumns+` FROM t_factor_udf_versions WHERE c_name = ? ORDER BY c_ctime DESC, c_hash`, name).Scan(&rows).Error; err != nil {
		return UdfName{}, nil, err
	}
	versions := make([]UdfVersion, len(rows))
	for i, r := range rows {
		versions[i] = r.version()
	}
	return UdfName{Name: row.Name, ActiveHash: row.ActiveHash, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, versions, nil
}

// ListUdfs 按名称返回全部自定义算子。
func (s *Store) ListUdfs(ctx context.Context) ([]UdfName, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	var rows []struct {
		Name       string    `gorm:"column:c_name"`
		ActiveHash string    `gorm:"column:c_active_hash"`
		CreatedAt  time.Time `gorm:"column:c_ctime"`
		UpdatedAt  time.Time `gorm:"column:c_mtime"`
	}
	if err := s.db.WithContext(ctx).Raw(`SELECT c_name, c_active_hash, c_ctime, c_mtime FROM t_factor_udfs ORDER BY c_name`).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]UdfName, len(rows))
	for i, r := range rows {
		out[i] = UdfName{Name: r.Name, ActiveHash: r.ActiveHash, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	}
	return out, nil
}

// SetActiveUdf 切换名称启用的版本（hash 为空表示清空）：版本必须属于这个名称；已被因子定义引用的名称不能清空。
// 在事务里校验并修改，调用方持有全局 UDF 锁。
func (s *Store) SetActiveUdf(ctx context.Context, name, hash string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if hash != "" {
			var owner string
			result := tx.Raw(`SELECT c_name FROM t_factor_udf_versions WHERE c_hash = ?`, hash).Scan(&owner)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 || owner != name {
				return fmt.Errorf("%w: version %s is not a version of udf %q", ErrConflict, hash, name)
			}
		} else {
			refs, err := udfReferences(ctx, tx, name)
			if err != nil {
				return err
			}
			if len(refs) > 0 {
				return fmt.Errorf("%w: udf %q is referenced by factor definitions (%s); it cannot be cleared", ErrConflict, name, strings.Join(refs, ", "))
			}
		}
		result := tx.Exec(`UPDATE t_factor_udfs SET c_active_hash = ?, c_mtime = ? WHERE c_name = ?`, hash, time.Now().UTC(), name)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// UdfReferences 返回引用某个自定义算子名称的因子定义 ID（按定义文本扫描，只会多、不会漏）。
func (s *Store) UdfReferences(ctx context.Context, name string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	return udfReferences(ctx, s.db, name)
}

func udfReferences(ctx context.Context, db *gorm.DB, name string) ([]string, error) {
	var rows []struct {
		ID   string `gorm:"column:c_factor_id"`
		Spec string `gorm:"column:c_spec"`
	}
	if err := db.WithContext(ctx).Raw(`SELECT c_factor_id, c_spec FROM t_factor_defs WHERE c_spec LIKE ? ORDER BY c_factor_id`, "%"+name+"%").Scan(&rows).Error; err != nil {
		return nil, err
	}
	var ids []string
	for _, row := range rows {
		for _, ref := range domain.UDFNames(row.Spec) {
			if ref == name {
				ids = append(ids, row.ID)
				break
			}
		}
	}
	return ids, nil
}

// ---------------------------------------------------------------- 绑定解析

type pinsKey struct{}

// WithUDFPins 返回带固定绑定（名称 → 版本哈希）的上下文：读取定义时，pins 里的名称按固定版本解析而不是当前启用版本。
// 补算作业用它：首次领取时固化的绑定在续跑、重领、重启后都不变。
func WithUDFPins(ctx context.Context, pins map[string]string) context.Context {
	if len(pins) == 0 {
		return ctx
	}
	return context.WithValue(ctx, pinsKey{}, pins)
}

func pinsFrom(ctx context.Context) map[string]string {
	pins, _ := ctx.Value(pinsKey{}).(map[string]string)
	return pins
}

// ResolveUDFs 按名称解析自定义算子（固定绑定优先，否则取当前启用版本）。没有启用版本的名称带空哈希返回，
// 编译时据此报“没有启用的版本”；不存在的名称不在结果里。overrides 是请求级覆盖（名称 → 版本哈希），优先于固定绑定与启用版本。
func (s *Store) ResolveUDFs(ctx context.Context, names []string, overrides map[string]string) (map[string]factorexpr.UDF, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	return resolveUDFs(ctx, s.db, names, overrides)
}

func resolveUDFs(ctx context.Context, db *gorm.DB, names []string, overrides map[string]string) (map[string]factorexpr.UDF, error) {
	if len(names) == 0 {
		return nil, nil
	}
	pins := pinsFrom(ctx)
	out := make(map[string]factorexpr.UDF, len(names))
	for _, name := range names {
		hash := overrides[name]
		if hash == "" {
			hash = pins[name]
		}
		if hash == "" {
			var active string
			result := db.WithContext(ctx).Raw(`SELECT c_active_hash FROM t_factor_udfs WHERE c_name = ?`, name).Scan(&active)
			if result.Error != nil {
				return nil, result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			if active == "" {
				out[name] = factorexpr.UDF{Name: name}
				continue
			}
			hash = active
		}
		v, err := getUdfVersion(ctx, db, hash, false)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("udf %s: version %s does not exist", name, hash)
		}
		if err != nil {
			return nil, err
		}
		if v.Name != name {
			return nil, fmt.Errorf("udf %s: version %s belongs to udf %q", name, hash, v.Name)
		}
		u, err := UDFFromVersion(v)
		if err != nil {
			return nil, err
		}
		out[name] = u
	}
	return out, nil
}

// UDFFromVersion 把存储的版本转换成 factorexpr 的自定义算子定义。
func UDFFromVersion(v UdfVersion) (factorexpr.UDF, error) {
	var manifest factorudf.PackageManifest
	if err := json.Unmarshal([]byte(v.Manifest), &manifest); err != nil {
		return factorexpr.UDF{}, fmt.Errorf("udf %s version %s: manifest is not valid JSON: %w", v.Name, v.Hash, err)
	}
	return udfhost.UDFFromManifest(&manifest.Manifest, v.Hash, udfhost.Describe{Doc: manifest.Doc, Example: manifest.Example})
}

// EnabledUDFEnv 返回全部已启用版本的解析环境（函数参考与控制台补全用）。
func (s *Store) EnabledUDFEnv(ctx context.Context) (factorexpr.UDFs, error) {
	names, err := s.ListUdfs(ctx)
	if err != nil {
		return nil, err
	}
	var list []string
	for _, n := range names {
		if n.ActiveHash != "" {
			list = append(list, n.Name)
		}
	}
	sort.Strings(list)
	resolved, err := s.ResolveUDFs(ctx, list, nil)
	if err != nil {
		return nil, err
	}
	return factorexpr.UDFs(resolved), nil
}

// resolveDef 还原定义并编译：先扫描定义文本里引用的自定义算子，按固定绑定或启用版本解析，再带绑定编译
// （设计 4.3：读取顺序是读原文 → 解析绑定 → 带绑定编译）。解析失败时不带绑定编译，编译错误由调用方的校验报告。
func (s *Store) resolveDef(ctx context.Context, db *gorm.DB, row factorDefRow) (domain.FactorDef, error) {
	def := domain.FactorDef{
		FactorID: row.FactorID, Name: row.Name, CategoryID: row.CategoryID, Spec: row.Spec, SpecHash: row.SpecHash,
		AllowPartialUniverse: row.AllowPartialUniverse != 0, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if names := domain.UDFNames(row.Spec); len(names) > 0 {
		udfs, err := resolveUDFs(ctx, db, names, nil)
		if err != nil {
			return def, fmt.Errorf("resolve udfs of factor %s: %w", row.FactorID, err)
		}
		def.UDFs = udfs
	}
	if compiled, err := domain.CompileDefinition(def); err == nil {
		def = compiled
	}
	return def, nil
}

// ---------------------------------------------------------------- 补算作业的绑定

// JobBindings 返回补算作业首次领取时固化的绑定（名称 → 版本哈希）；还没有固化时 found 为假。
func (s *Store) JobBindings(ctx context.Context, jobID string) (bindings map[string]string, found bool, err error) {
	if s == nil || s.db == nil {
		return nil, false, fmt.Errorf("factor database is not open")
	}
	var raw string
	result := s.db.WithContext(ctx).Raw(`SELECT c_bindings FROM t_factor_recalc_job_bindings WHERE c_job_id = ?`, jobID).Scan(&raw)
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, false, nil
	}
	if err := json.Unmarshal([]byte(raw), &bindings); err != nil {
		return nil, false, fmt.Errorf("job %s bindings are not valid JSON: %w", jobID, err)
	}
	return bindings, true, nil
}

// SaveJobBindings 固化补算作业的绑定；已经固化过的不覆盖（重新领取、续跑都用第一次的），返回生效的绑定。
func (s *Store) SaveJobBindings(ctx context.Context, jobID string, bindings map[string]string) (map[string]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	raw, err := json.Marshal(bindings)
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Exec(`INSERT OR IGNORE INTO t_factor_recalc_job_bindings (c_job_id, c_bindings) VALUES (?, ?)`, jobID, string(raw)).Error; err != nil {
		return nil, err
	}
	effective, found, err := s.JobBindings(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("job %s bindings were not saved", jobID)
	}
	return effective, nil
}

// UDFBindingsOf 汇总成员定义引用的自定义算子版本（名称 → 版本哈希）；编译失败的定义不贡献绑定。
func UDFBindingsOf(members []domain.MemberDef) map[string]string {
	out := map[string]string{}
	for _, member := range members {
		if compiled := member.Factor.Compiled(); compiled != nil {
			for _, ref := range compiled.UDFs {
				out[ref.Name] = ref.Hash
			}
		}
	}
	return out
}
