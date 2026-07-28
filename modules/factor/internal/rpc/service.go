package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/pyruntime/moduleregistry"
	"trpc.group/trpc-go/trpc-go/log"
)

var _ factorpb.FactorMgrService = (*Service)(nil)

var pythonModuleNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type schedulerRuntime interface {
	Status() scheduler.Status
	Run(context.Context, scheduler.Task) error
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
	factors    *store.FactorRepository
	bindings   *store.BindingRepository
	scheduler  schedulerRuntime
	factorsDir string
	publisher  *moduleregistry.SourcePublisher
	meta       *registry.MetadataSync
	mutationMu sync.Mutex
}

// NewWithRuntime creates a FactorMgr service with an optional scheduler runtime.
func NewWithRuntime(persistence *store.Store, sched schedulerRuntime, opts ...Option) *Service {
	s := &Service{
		factors:    persistence.Factors(),
		bindings:   persistence.Bindings(),
		scheduler:  sched,
		factorsDir: "./factors",
	}
	for _, opt := range opts {
		opt(s)
	}
	s.publisher = moduleregistry.NewSourcePublisher(filepath.Join(s.factorsDir, ".versions"))
	return s
}

func (s *Service) CreateFactor(ctx context.Context, req *factorpb.CreateFactorReq) (*factorpb.CreateFactorRsp, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	factor, err := s.normalizeFactor(req.GetFactor())
	if err != nil {
		return &factorpb.CreateFactorRsp{RetInfo: invalid(err)}, nil
	}
	if _, err := s.factors.Get(ctx, factor.FactorID); err == nil {
		return &factorpb.CreateFactorRsp{RetInfo: invalid(fmt.Errorf("factor_id %q already exists", factor.FactorID))}, nil
	}
	if _, err := s.factors.GetByName(ctx, factor.Name); err == nil {
		return &factorpb.CreateFactorRsp{RetInfo: invalid(fmt.Errorf("factor name %q already exists", factor.Name))}, nil
	}
	if err := s.publishFactorSource(ctx, &factor); err != nil {
		return &factorpb.CreateFactorRsp{RetInfo: inner(err)}, nil
	}
	if err := s.factors.Create(ctx, factor); err != nil {
		return &factorpb.CreateFactorRsp{RetInfo: inner(err)}, nil
	}
	if err := s.syncFactorBindings(ctx, factor.FactorID); err != nil {
		return &factorpb.CreateFactorRsp{RetInfo: inner(err)}, nil
	}
	got, _ := s.factors.Get(ctx, factor.FactorID)
	return &factorpb.CreateFactorRsp{RetInfo: success(), Factor: factorToPB(*got)}, nil
}

func (s *Service) UpdateFactor(ctx context.Context, req *factorpb.UpdateFactorReq) (*factorpb.UpdateFactorRsp, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	factorPB := req.GetFactor()
	if factorPB != nil && factorPB.GetFactorId() == "" {
		factorPB.FactorId = req.GetFactorId()
	}
	factor, err := s.normalizeFactor(factorPB)
	if err != nil {
		return &factorpb.UpdateFactorRsp{RetInfo: invalid(err)}, nil
	}
	existing, err := s.factors.Get(ctx, factor.FactorID)
	if err != nil {
		return &factorpb.UpdateFactorRsp{RetInfo: inner(err)}, nil
	}
	if !slices.Equal(existing.Outputs, factor.Outputs) {
		return &factorpb.UpdateFactorRsp{RetInfo: invalid(fmt.Errorf("factor outputs are immutable; create a new factor_id"))}, nil
	}
	if sameName, err := s.factors.GetByName(ctx, factor.Name); err == nil && sameName.FactorID != factor.FactorID {
		return &factorpb.UpdateFactorRsp{RetInfo: invalid(fmt.Errorf("factor name %q already exists", factor.Name))}, nil
	}
	if err := s.publishFactorSource(ctx, &factor); err != nil {
		return &factorpb.UpdateFactorRsp{RetInfo: inner(err)}, nil
	}
	if err := s.factors.Update(ctx, factor); err != nil {
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
	page, size := pageParams(req.GetPage())
	rows, total, err := s.factors.List(ctx, store.FactorFilter{Status: req.GetStatus(), Page: store.Page{Page: int(page), PageSize: int(size)}})
	if err != nil {
		return &factorpb.ListFactorsRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*factorpb.FactorDef, 0, len(rows))
	for _, row := range rows {
		out = append(out, factorToPB(row))
	}
	return &factorpb.ListFactorsRsp{RetInfo: success(), Factors: out, PageResult: pageResult(page, size, total)}, nil
}

func (s *Service) SetFactorStatus(ctx context.Context, req *factorpb.SetFactorStatusReq) (*factorpb.SetFactorStatusRsp, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if req.GetFactorId() == "" || req.GetStatus() == "" {
		return &factorpb.SetFactorStatusRsp{RetInfo: invalid(fmt.Errorf("factor_id and status are required"))}, nil
	}
	if err := s.factors.SetStatus(ctx, req.GetFactorId(), req.GetStatus()); err != nil {
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

func (s *Service) DeleteFactor(ctx context.Context, req *factorpb.DeleteFactorReq) (*factorpb.DeleteFactorRsp, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if strings.TrimSpace(req.GetFactorId()) == "" {
		return &factorpb.DeleteFactorRsp{RetInfo: invalid(fmt.Errorf("factor_id is required"))}, nil
	}
	factor, err := s.factors.Get(ctx, req.GetFactorId())
	if err != nil {
		return &factorpb.DeleteFactorRsp{RetInfo: inner(err)}, nil
	}
	bindings, err := s.bindings.ListByFactor(ctx, factor.FactorID)
	if err != nil {
		return &factorpb.DeleteFactorRsp{RetInfo: inner(err)}, nil
	}
	if len(bindings) != 0 {
		return &factorpb.DeleteFactorRsp{RetInfo: invalid(fmt.Errorf("factor %q still has bindings; delete them first", factor.FactorID))}, nil
	}
	stage, err := stageFactorArtifacts(s.factorsDir, factor.Name)
	if err != nil {
		return &factorpb.DeleteFactorRsp{RetInfo: inner(err)}, nil
	}
	if err := s.factors.Delete(ctx, factor.FactorID); err != nil {
		return &factorpb.DeleteFactorRsp{RetInfo: inner(errors.Join(err, stage.Restore()))}, nil
	}
	if err := stage.Remove(); err != nil {
		log.Errorf("remove deleted factor %s staged artifacts: %v", factor.FactorID, err)
	}
	return &factorpb.DeleteFactorRsp{RetInfo: success()}, nil
}

func (s *Service) UpsertBinding(ctx context.Context, req *factorpb.UpsertBindingReq) (*factorpb.UpsertBindingRsp, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	binding, err := s.normalizeBinding(req.GetBinding())
	if err != nil {
		return &factorpb.UpsertBindingRsp{RetInfo: invalid(err)}, nil
	}
	if binding.Status == domain.BindingStatusDisabled {
		if err := s.bindings.Upsert(ctx, binding); err != nil {
			return &factorpb.UpsertBindingRsp{RetInfo: inner(err)}, nil
		}
		if err := s.syncBindingMetadata(ctx, binding); err != nil {
			return &factorpb.UpsertBindingRsp{RetInfo: inner(err)}, nil
		}
		return &factorpb.UpsertBindingRsp{RetInfo: success(), Binding: bindingToPB(binding)}, nil
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
	page, size := pageParams(req.GetPage())
	rows, total, err := s.bindings.List(ctx, store.BindingFilter{SpaceID: req.GetSpaceId(), SourceDataset: req.GetSourceDataset(), Freq: req.GetFreq(), Status: req.GetStatus(), Page: store.Page{Page: int(page), PageSize: int(size)}})
	if err != nil {
		return &factorpb.ListBindingsRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*factorpb.FactorBinding, 0, len(rows))
	for _, row := range rows {
		out = append(out, bindingToPB(row))
	}
	return &factorpb.ListBindingsRsp{RetInfo: success(), Bindings: out, PageResult: pageResult(page, size, total)}, nil
}

func (s *Service) DeleteBinding(ctx context.Context, req *factorpb.DeleteBindingReq) (*factorpb.DeleteBindingRsp, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if req.GetBindingId() == "" {
		return &factorpb.DeleteBindingRsp{RetInfo: invalid(fmt.Errorf("binding_id is required"))}, nil
	}
	if err := s.bindings.Delete(ctx, req.GetBindingId()); err != nil {
		return &factorpb.DeleteBindingRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.DeleteBindingRsp{RetInfo: success()}, nil
}

func (s *Service) GetEngineStatus(context.Context, *factorpb.GetEngineStatusReq) (*factorpb.GetEngineStatusRsp, error) {
	rsp := &factorpb.GetEngineStatusRsp{RetInfo: success()}
	if s.scheduler != nil {
		status := s.scheduler.Status()
		rsp.QueueDepth = int32(status.QueueDepth)
		rsp.QueueOverflowCount = status.QueueOverflowCount
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
	sum := sha256.Sum256([]byte(factor.SourceCode))
	factor.SourceHash = hex.EncodeToString(sum[:])
	return domain.NormalizeFactorDefinition(factor)
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
	subjectsJSON, err := domain.NormalizeBindingSubjects(binding.SubjectMode, binding.SubjectsJSON)
	if err != nil {
		return domain.FactorBinding{}, err
	}
	binding.SubjectsJSON = subjectsJSON
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
	bindings, err := s.bindings.ListByFactor(ctx, factorID)
	if err != nil {
		return err
	}
	spaces := make([]string, 0, len(bindings))
	seenSpaces := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if _, exists := seenSpaces[binding.SpaceID]; exists {
			continue
		}
		seenSpaces[binding.SpaceID] = struct{}{}
		spaces = append(spaces, binding.SpaceID)
	}
	slices.Sort(spaces)
	for _, spaceID := range spaces {
		if err := s.meta.SyncFactorMetadata(ctx, spaceID, *factor); err != nil {
			return err
		}
	}
	if factor.Status != domain.FactorStatusEnabled {
		return nil
	}
	for _, binding := range bindings {
		if binding.Status != domain.BindingStatusEnabled {
			continue
		}
		if err := s.syncEnabledBindingTarget(ctx, binding, *factor); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) syncBindingMetadata(ctx context.Context, binding domain.FactorBinding) error {
	if s.meta == nil {
		return nil
	}
	factor, err := s.factors.Get(ctx, binding.FactorID)
	if err != nil {
		return err
	}
	if err := s.meta.SyncFactorMetadata(ctx, binding.SpaceID, *factor); err != nil {
		return err
	}
	if binding.Status != domain.BindingStatusEnabled || factor.Status != domain.FactorStatusEnabled {
		return nil
	}
	return s.syncEnabledBindingTarget(ctx, binding, *factor)
}

func (s *Service) syncEnabledBindingTarget(ctx context.Context, binding domain.FactorBinding, factor domain.FactorDef) error {
	targetDataset := binding.TargetDataset
	if targetDataset == "" {
		targetDataset = registry.ResultDataset(binding.SourceDataset)
	}
	return s.meta.SyncTargetDatasetAfterFactorMetadata(
		ctx,
		binding.SpaceID,
		binding.SourceDataset,
		targetDataset,
		binding.Freq,
		[]domain.FactorDef{factor},
	)
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
