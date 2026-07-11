package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	"github.com/mooyang-code/moox/modules/factor/internal/repository"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/pyruntime/moduleregistry"
	"gorm.io/gorm"
)

var _ factorpb.FactorMgrService = (*Service)(nil)

var pythonModuleNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type schedulerRuntime interface {
	Status() scheduler.Status
	Enqueue(context.Context, scheduler.Task)
	Drain(context.Context) error
}

type engineStatusProvider interface {
	Status() engine.WorkerPoolStatus
}

// Option customizes a FactorMgr service.
type Option func(*Service)

// WithFactorsDir sets the Python factor source directory used by RPC writes.
func WithFactorsDir(dir string) Option {
	return func(s *Service) {
		if strings.TrimSpace(dir) != "" {
			s.factorsDir = dir
		}
	}
}

// WithMetadataSync mirrors enabled bindings into Storage Metadata after edits.
func WithMetadataSync(syncer *registry.MetadataSync) Option {
	return func(s *Service) {
		s.meta = syncer
	}
}

// Service implements FactorMgr.
type Service struct {
	db         *gorm.DB
	factors    *repository.FactorRepository
	bindings   *repository.BindingRepository
	scheduler  schedulerRuntime
	engine     engineStatusProvider
	factorsDir string
	publisher  *moduleregistry.SourcePublisher
	meta       *registry.MetadataSync
	recalcMu   sync.Mutex
	recalc     map[string]*recalcState
}

// New creates a FactorMgr service.
func New(db *gorm.DB) *Service {
	return NewWithRuntime(db, nil, nil)
}

// NewWithRuntime creates a FactorMgr service with optional runtime status providers.
func NewWithRuntime(db *gorm.DB, sched schedulerRuntime, eng engineStatusProvider, opts ...Option) *Service {
	s := &Service{
		db:         db,
		factors:    repository.NewFactorRepository(db),
		bindings:   repository.NewBindingRepository(db),
		scheduler:  sched,
		engine:     eng,
		factorsDir: "./factors",
		recalc:     map[string]*recalcState{},
	}
	for _, opt := range opts {
		opt(s)
	}
	s.publisher = moduleregistry.NewSourcePublisher(filepath.Join(s.factorsDir, ".versions"))
	return s
}

func (s *Service) CreateFactor(ctx context.Context, req *factorpb.CreateFactorReq) (*factorpb.CreateFactorRsp, error) {
	factor, err := s.normalizeFactor(req.GetFactor())
	if err != nil {
		return &factorpb.CreateFactorRsp{RetInfo: invalid(err)}, nil
	}
	if existing, err := s.factors.GetByName(ctx, factor.Name); err == nil && existing.FactorID != factor.FactorID {
		return &factorpb.CreateFactorRsp{RetInfo: invalid(fmt.Errorf("factor name %q already exists", factor.Name))}, nil
	}
	if err := s.publishFactorSource(ctx, &factor); err != nil {
		return &factorpb.CreateFactorRsp{RetInfo: inner(err)}, nil
	}
	if err := s.factors.Upsert(ctx, factor); err != nil {
		return &factorpb.CreateFactorRsp{RetInfo: inner(err)}, nil
	}
	if err := s.syncFactorBindings(ctx, factor.FactorID); err != nil {
		return &factorpb.CreateFactorRsp{RetInfo: inner(err)}, nil
	}
	got, _ := s.factors.Get(ctx, factor.FactorID)
	return &factorpb.CreateFactorRsp{RetInfo: success(), Factor: factorToPB(*got)}, nil
}

func (s *Service) UpdateFactor(ctx context.Context, req *factorpb.UpdateFactorReq) (*factorpb.UpdateFactorRsp, error) {
	factorPB := req.GetFactor()
	if factorPB != nil && factorPB.GetFactorId() == "" {
		factorPB.FactorId = req.GetFactorId()
	}
	factor, err := s.normalizeFactor(factorPB)
	if err != nil {
		return &factorpb.UpdateFactorRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.publishFactorSource(ctx, &factor); err != nil {
		return &factorpb.UpdateFactorRsp{RetInfo: inner(err)}, nil
	}
	if err := s.factors.Upsert(ctx, factor); err != nil {
		return &factorpb.UpdateFactorRsp{RetInfo: inner(err)}, nil
	}
	if err := s.syncFactorBindings(ctx, factor.FactorID); err != nil {
		return &factorpb.UpdateFactorRsp{RetInfo: inner(err)}, nil
	}
	got, _ := s.factors.Get(ctx, factor.FactorID)
	return &factorpb.UpdateFactorRsp{RetInfo: success(), Factor: factorToPB(*got)}, nil
}

func (s *Service) publishFactorSource(ctx context.Context, factor *domain.FactorDef) error {
	if factor == nil {
		return fmt.Errorf("factor is required")
	}
	if s.publisher != nil {
		version, err := s.publisher.Publish(ctx, moduleregistry.ModuleSource{Type: "factor", LogicalID: factor.Name, Source: []byte(factor.SourceCode)})
		if err != nil {
			return err
		}
		factor.SourceHash = version.SourceHash
		factor.SourcePath = version.Path
	}
	return s.writeFactorSource(*factor)
}

func (s *Service) GetFactor(ctx context.Context, req *factorpb.GetFactorReq) (*factorpb.GetFactorRsp, error) {
	if strings.TrimSpace(req.GetFactorId()) == "" {
		return &factorpb.GetFactorRsp{RetInfo: invalid(fmt.Errorf("factor_id is required"))}, nil
	}
	factor, err := s.factors.Get(ctx, req.GetFactorId())
	if err != nil {
		return &factorpb.GetFactorRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.GetFactorRsp{RetInfo: success(), Factor: factorToPB(*factor)}, nil
}

func (s *Service) ListFactors(ctx context.Context, req *factorpb.ListFactorsReq) (*factorpb.ListFactorsRsp, error) {
	q := s.db.WithContext(ctx).Model(&domain.FactorDef{})
	if req.GetKind() != "" {
		q = q.Where("c_kind = ?", req.GetKind())
	}
	if req.GetStatus() != "" {
		q = q.Where("c_status = ?", req.GetStatus())
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return &factorpb.ListFactorsRsp{RetInfo: inner(err)}, nil
	}
	page, size := pageParams(req.GetPage())
	var rows []domain.FactorDef
	if err := q.Order("c_mtime DESC").Offset(int((page - 1) * size)).Limit(int(size)).Find(&rows).Error; err != nil {
		return &factorpb.ListFactorsRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*factorpb.FactorDef, 0, len(rows))
	for _, row := range rows {
		out = append(out, factorToPB(row))
	}
	return &factorpb.ListFactorsRsp{RetInfo: success(), Factors: out, PageResult: pageResult(page, size, total)}, nil
}

func (s *Service) SetFactorStatus(ctx context.Context, req *factorpb.SetFactorStatusReq) (*factorpb.SetFactorStatusRsp, error) {
	if req.GetFactorId() == "" || req.GetStatus() == "" {
		return &factorpb.SetFactorStatusRsp{RetInfo: invalid(fmt.Errorf("factor_id and status are required"))}, nil
	}
	if err := s.db.WithContext(ctx).Model(&domain.FactorDef{}).Where("c_factor_id = ?", req.GetFactorId()).Updates(map[string]any{
		"c_status": req.GetStatus(),
		"c_mtime":  time.Now().UTC(),
	}).Error; err != nil {
		return &factorpb.SetFactorStatusRsp{RetInfo: inner(err)}, nil
	}
	got, err := s.factors.Get(ctx, req.GetFactorId())
	if err != nil {
		return &factorpb.SetFactorStatusRsp{RetInfo: inner(err)}, nil
	}
	if err := s.syncFactorBindings(ctx, req.GetFactorId()); err != nil {
		return &factorpb.SetFactorStatusRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.SetFactorStatusRsp{RetInfo: success(), Factor: factorToPB(*got)}, nil
}

func (s *Service) UpsertBinding(ctx context.Context, req *factorpb.UpsertBindingReq) (*factorpb.UpsertBindingRsp, error) {
	binding, err := s.normalizeBinding(req.GetBinding())
	if err != nil {
		return &factorpb.UpsertBindingRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.syncBindingMetadata(ctx, binding); err != nil {
		return &factorpb.UpsertBindingRsp{RetInfo: inner(err)}, nil
	}
	if err := s.bindings.Upsert(ctx, binding); err != nil {
		return &factorpb.UpsertBindingRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.UpsertBindingRsp{RetInfo: success(), Binding: bindingToPB(binding)}, nil
}

func (s *Service) ListBindings(ctx context.Context, req *factorpb.ListBindingsReq) (*factorpb.ListBindingsRsp, error) {
	q := s.db.WithContext(ctx).Model(&domain.FactorBinding{})
	if req.GetSpaceId() != "" {
		q = q.Where("c_space_id = ?", req.GetSpaceId())
	}
	if req.GetSourceDataset() != "" {
		q = q.Where("c_source_dataset = ?", req.GetSourceDataset())
	}
	if req.GetFreq() != "" {
		q = q.Where("c_freq = ?", req.GetFreq())
	}
	if req.GetStatus() != "" {
		q = q.Where("c_status = ?", req.GetStatus())
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return &factorpb.ListBindingsRsp{RetInfo: inner(err)}, nil
	}
	page, size := pageParams(req.GetPage())
	var rows []domain.FactorBinding
	if err := q.Order("c_mtime DESC").Offset(int((page - 1) * size)).Limit(int(size)).Find(&rows).Error; err != nil {
		return &factorpb.ListBindingsRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*factorpb.FactorBinding, 0, len(rows))
	for _, row := range rows {
		out = append(out, bindingToPB(row))
	}
	return &factorpb.ListBindingsRsp{RetInfo: success(), Bindings: out, PageResult: pageResult(page, size, total)}, nil
}

func (s *Service) DeleteBinding(ctx context.Context, req *factorpb.DeleteBindingReq) (*factorpb.DeleteBindingRsp, error) {
	if req.GetBindingId() == "" {
		return &factorpb.DeleteBindingRsp{RetInfo: invalid(fmt.Errorf("binding_id is required"))}, nil
	}
	if err := s.db.WithContext(ctx).Where("c_binding_id = ?", req.GetBindingId()).Delete(&domain.FactorBinding{}).Error; err != nil {
		return &factorpb.DeleteBindingRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.DeleteBindingRsp{RetInfo: success()}, nil
}

func (s *Service) ListFactorRuns(ctx context.Context, req *factorpb.ListFactorRunsReq) (*factorpb.ListFactorRunsRsp, error) {
	page, size := pageParams(req.GetPage())
	return &factorpb.ListFactorRunsRsp{RetInfo: success(), Runs: []*factorpb.FactorRun{}, PageResult: pageResult(page, size, 0)}, nil
}

func (s *Service) GetEngineStatus(context.Context, *factorpb.GetEngineStatusReq) (*factorpb.GetEngineStatusRsp, error) {
	rsp := &factorpb.GetEngineStatusRsp{RetInfo: success()}
	if s.scheduler != nil {
		status := s.scheduler.Status()
		rsp.QueueDepth = int32(status.QueueDepth)
		rsp.SupersedeCount = status.SupersedeCount
		rsp.WritebackFailures = status.WritebackFailures
	}
	if s.engine != nil {
		status := s.engine.Status()
		rsp.Workers = make([]*factorpb.WorkerStatus, 0, status.Workers)
		for i := 0; i < status.Workers; i++ {
			rsp.Workers = append(rsp.Workers, &factorpb.WorkerStatus{
				WorkerId: fmt.Sprintf("worker-%d", i+1),
				State:    "ready",
			})
		}
	}
	return rsp, nil
}

func (s *Service) normalizeFactor(pb *factorpb.FactorDef) (domain.FactorDef, error) {
	if pb == nil {
		return domain.FactorDef{}, fmt.Errorf("factor is required")
	}
	factor := factorFromPB(pb)
	if factor.FactorID == "" || factor.Name == "" || factor.SourceCode == "" {
		return domain.FactorDef{}, fmt.Errorf("factor_id, name and source_code are required")
	}
	if !pythonModuleNamePattern.MatchString(factor.Name) {
		return domain.FactorDef{}, fmt.Errorf("factor name %q must be a valid Python module name", factor.Name)
	}
	if factor.Kind == "" {
		factor.Kind = domain.FactorKindTimeseries
	}
	if factor.ParamsJSON == "" {
		factor.ParamsJSON = "[]"
	}
	if factor.DependsJSON == "" || strings.TrimSpace(factor.DependsJSON) == "[]" {
		factor.DependsJSON = registry.DependsJSONFromSource(factor.SourceCode)
	}
	if factor.LookbackBars == 0 {
		factor.LookbackBars = registry.DefaultLookback(nil)
	}
	if factor.WritebackBars == 0 {
		factor.WritebackBars = 5
	}
	if factor.Status == "" {
		factor.Status = domain.FactorStatusDisabled
	}
	if factor.SourceHash == "" {
		sum := sha256.Sum256([]byte(factor.SourceCode))
		factor.SourceHash = hex.EncodeToString(sum[:])
	}
	return factor, nil
}

func (s *Service) normalizeBinding(pb *factorpb.FactorBinding) (domain.FactorBinding, error) {
	if pb == nil {
		return domain.FactorBinding{}, fmt.Errorf("binding is required")
	}
	binding := bindingFromPB(pb)
	if binding.FactorID == "" || binding.SpaceID == "" || binding.SourceDataset == "" || binding.Freq == "" {
		return domain.FactorBinding{}, fmt.Errorf("factor_id, space_id, source_dataset and freq are required")
	}
	if binding.BindingID == "" {
		binding.BindingID = fmt.Sprintf("bind-%d", time.Now().UnixNano())
	}
	if binding.SubjectMode == "" {
		binding.SubjectMode = domain.SubjectModeAll
	}
	if binding.SubjectsJSON == "" {
		binding.SubjectsJSON = "[]"
	}
	if binding.TargetDataset == "" {
		binding.TargetDataset = registry.ResultDataset(binding.SourceDataset)
	}
	if binding.Status == "" {
		binding.Status = domain.BindingStatusEnabled
	}
	return binding, nil
}

func (s *Service) writeFactorSource(factor domain.FactorDef) error {
	if strings.TrimSpace(s.factorsDir) == "" {
		return fmt.Errorf("factors directory is not configured")
	}
	if !pythonModuleNamePattern.MatchString(factor.Name) {
		return fmt.Errorf("factor name %q must be a valid Python module name", factor.Name)
	}
	if err := os.MkdirAll(s.factorsDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(s.factorsDir, factor.Name+".py")
	tmp, err := os.CreateTemp(s.factorsDir, ".factor-*.py")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err = tmp.Write([]byte(factor.SourceCode)); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (s *Service) syncFactorBindings(ctx context.Context, factorID string) error {
	if s.meta == nil {
		return nil
	}
	factor, err := s.factors.Get(ctx, factorID)
	if err != nil {
		return err
	}
	if factor.Status != domain.FactorStatusEnabled {
		return nil
	}
	var bindings []domain.FactorBinding
	if err := s.db.WithContext(ctx).
		Where("c_factor_id = ? AND c_status = ?", factorID, domain.BindingStatusEnabled).
		Find(&bindings).Error; err != nil {
		return err
	}
	for _, binding := range bindings {
		if err := s.syncBindingMetadata(ctx, binding); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) syncBindingMetadata(ctx context.Context, binding domain.FactorBinding) error {
	if s.meta == nil || binding.Status != domain.BindingStatusEnabled {
		return nil
	}
	factor, err := s.factors.Get(ctx, binding.FactorID)
	if err != nil {
		return err
	}
	if factor.Status != domain.FactorStatusEnabled {
		return nil
	}
	targetDataset := binding.TargetDataset
	if targetDataset == "" {
		targetDataset = registry.ResultDataset(binding.SourceDataset)
	}
	return s.meta.SyncTargetDataset(ctx, binding.SpaceID, binding.SourceDataset, targetDataset, binding.Freq, []domain.FactorDef{*factor})
}

func success() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}
}

func invalid(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: err.Error()}
}

func inner(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: err.Error()}
}

func pageParams(page *commonpb.Page) (uint32, uint32) {
	pageNo, size := page.GetPage(), page.GetSize()
	if pageNo == 0 {
		pageNo = 1
	}
	if size == 0 {
		size = 50
	}
	return pageNo, size
}

func pageResult(page uint32, size uint32, total int64) *commonpb.PageResult {
	return &commonpb.PageResult{Page: page, Size: size, Total: uint32(total), HasMore: uint32(total) > page*size}
}
