package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const defaultRetiredIndexAge = time.Minute

type managedIndexRef struct {
	engine  string
	indexID string
}

type retiredIndexCandidate struct {
	firstSeen  time.Time
	lastSeen   time.Time
	generation uint64
}

type RetiredIndexCleanupOptions struct {
	Metadata           MetadataClient
	MinUnreferencedAge time.Duration
	Now                func() time.Time
}

func (s *Service) CleanupRetiredIndexes(ctx context.Context, opts RetiredIndexCleanupOptions) error {
	if opts.Metadata == nil {
		return errors.New("retired view index cleanup requires Metadata")
	}
	age := opts.MinUnreferencedAge
	if age <= 0 {
		age = defaultRetiredIndexAge
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}

	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()

	protected, err := s.cleanupProtectedIndexes(ctx, opts.Metadata)
	if err != nil {
		s.resetCleanupCandidates()
		return fmt.Errorf("discover protected view indexes: %w", err)
	}

	engines := s.cleanupEngines()
	physical := make(map[managedIndexRef]struct{})
	listedEngines := make(map[string]struct{})
	var errs []error
	for _, engineName := range sortedEngineNames(engines) {
		engine := engines[engineName]
		lister, ok := engine.(viewindex.ManagedIndexLister)
		if !ok {
			continue
		}
		ids, listErr := lister.ListManagedIndexes(ctx)
		if listErr != nil {
			s.resetCleanupCandidatesForEngine(engineName)
			errs = append(errs, fmt.Errorf("list %s view indexes: %w", engineName, listErr))
			continue
		}
		listedEngines[engineName] = struct{}{}
		for _, id := range ids {
			physical[managedIndexRef{engine: engineName, indexID: id}] = struct{}{}
		}
	}
	s.clearMissingRetiringIndexes(physical, listedEngines)

	due := s.observeCleanupCandidates(now, age, physical, protected)
	if len(due) == 0 {
		return errors.Join(errs...)
	}

	// Re-read the authoritative state immediately before the destructive
	// phase. A failed read breaks continuous observation and resets candidate
	// age, so a Metadata outage can never age an index into deletion.
	revalidated, revalidateErr := s.cleanupProtectedIndexes(ctx, opts.Metadata)
	if revalidateErr != nil {
		s.resetCleanupCandidates()
		errs = append(errs, fmt.Errorf("revalidate protected view indexes: %w", revalidateErr))
		return errors.Join(errs...)
	}
	for _, ref := range due {
		if cleanupIndexProtected(revalidated, ref) {
			s.cancelCleanupCandidate(ref, "referenced_again")
			continue
		}
		if removeErr := s.removeRetiredIndex(ctx, ref, engines[ref.engine], opts.Metadata); removeErr != nil {
			errs = append(errs, removeErr)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) clearMissingRetiringIndexes(physical map[managedIndexRef]struct{}, listedEngines map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for indexID, generation := range s.retiringIndexes {
		engine := normalizedEngine(s.indexEngine[indexID])
		if engine == "" {
			continue
		}
		if _, listed := listedEngines[engine]; !listed {
			continue
		}
		if _, exists := physical[managedIndexRef{engine: engine, indexID: indexID}]; exists {
			continue
		}
		delete(s.retiringIndexes, indexID)
		log.Printf("storage view cleanup retirement cleared engine=%s index_id=%s generation=%d reason=physical_missing", engine, indexID, generation)
	}
}

func (s *Service) cleanupProtectedIndexes(ctx context.Context, metadata MetadataClient) (map[managedIndexRef]struct{}, error) {
	protected, err := metadataProtectedIndexes(ctx, metadata, s.internalAuth())
	if err != nil {
		return nil, err
	}
	type runtimeRef struct {
		view    viewRef
		runtime *viewRuntime
	}
	s.mu.RLock()
	runtimes := make([]runtimeRef, 0, len(s.views))
	for view, runtime := range s.views {
		if runtime != nil {
			runtimes = append(runtimes, runtimeRef{view: view, runtime: runtime})
		}
	}
	s.mu.RUnlock()
	for _, item := range runtimes {
		if !item.runtime.mu.TryLock() {
			// A busy runtime cannot be read safely. Protect both official slots for
			// only that View and continue collecting other Views, so one hot View
			// cannot prevent unrelated garbage from aging into cleanup.
			for _, slot := range []viewindex.Slot{viewindex.SlotA, viewindex.SlotB} {
				protected[managedIndexRef{indexID: viewindex.ViewIndexID(item.view.spaceID, item.view.viewID, slot)}] = struct{}{}
			}
			continue
		}
		if item.runtime.active != "" {
			protected[s.managedIndexRef(item.runtime.active)] = struct{}{}
		}
		if item.runtime.next != "" {
			protected[s.managedIndexRef(item.runtime.next)] = struct{}{}
		}
		item.runtime.mu.Unlock()
	}
	return protected, nil
}

func metadataProtectedIndexes(ctx context.Context, metadata MetadataClient, auth *pb.AuthInfo) (map[managedIndexRef]struct{}, error) {
	protected := make(map[managedIndexRef]struct{})
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := metadata.ListViews(ctx, &pb.ListViewsReq{
			AuthInfo: auth,
			Page:     &pb.Page{Page: pageNo, Size: 100},
		})
		if err != nil {
			return nil, err
		}
		if rsp == nil {
			return nil, errors.New("list Views returned nil response")
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, view := range rsp.GetViews() {
			if view == nil {
				continue
			}
			if id := strings.TrimSpace(view.GetActiveIndexId()); id != "" {
				protected[managedIndexRef{engine: normalizedEngine(view.GetEngine()), indexID: id}] = struct{}{}
			}
			if build := view.GetIndexBuild(); build != nil {
				if id := strings.TrimSpace(build.GetIndexId()); id != "" {
					engine := normalizedEngine(build.GetEngine())
					if engine == "" {
						engine = normalizedEngine(view.GetEngine())
					}
					protected[managedIndexRef{engine: engine, indexID: id}] = struct{}{}
				}
			}
		}
		pageResult := rsp.GetPageResult()
		if pageResult == nil {
			return nil, errors.New("list Views returned nil page result")
		}
		if pageResult.GetHasMore() && len(rsp.GetViews()) == 0 {
			return nil, errors.New("list Views returned an empty page with has_more=true")
		}
		if !pageResult.GetHasMore() {
			return protected, nil
		}
	}
}

func normalizedEngine(engine string) string {
	return strings.ToLower(strings.TrimSpace(engine))
}

func cleanupIndexProtected(protected map[managedIndexRef]struct{}, ref managedIndexRef) bool {
	if _, ok := protected[ref]; ok {
		return true
	}
	// Legacy Metadata/runtime rows may not have persisted an engine. Treat
	// those as a wildcard rather than risking deletion of the active artifact.
	_, ok := protected[managedIndexRef{indexID: ref.indexID}]
	return ok
}

func (s *Service) managedIndexRef(indexID string) managedIndexRef {
	s.mu.RLock()
	engine := normalizedEngine(s.indexEngine[indexID])
	s.mu.RUnlock()
	return managedIndexRef{engine: engine, indexID: indexID}
}

func (s *Service) cleanupEngines() map[string]viewindex.Engine {
	s.mu.RLock()
	defer s.mu.RUnlock()
	engines := make(map[string]viewindex.Engine, len(s.engines))
	for name, engine := range s.engines {
		engines[strings.ToLower(strings.TrimSpace(name))] = engine
	}
	return engines
}

func sortedEngineNames(engines map[string]viewindex.Engine) []string {
	names := make([]string, 0, len(engines))
	for name := range engines {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *Service) observeCleanupCandidates(now time.Time, age time.Duration, physical map[managedIndexRef]struct{}, protected map[managedIndexRef]struct{}) []managedIndexRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cleanupCandidates == nil {
		s.cleanupCandidates = make(map[managedIndexRef]retiredIndexCandidate)
	}
	for ref, candidate := range s.cleanupCandidates {
		if _, exists := physical[ref]; !exists {
			delete(s.cleanupCandidates, ref)
			if generation, ok := s.retiringIndexes[ref.indexID]; ok && generation == candidate.generation && (generation != s.indexGeneration[ref.indexID] || normalizedEngine(s.indexEngine[ref.indexID]) == ref.engine) {
				delete(s.retiringIndexes, ref.indexID)
			}
		}
	}
	due := make([]managedIndexRef, 0)
	for ref := range physical {
		if cleanupIndexProtected(protected, ref) {
			if _, ok := s.cleanupCandidates[ref]; ok {
				log.Printf("storage view cleanup candidate cancelled engine=%s index_id=%s reason=referenced_again", ref.engine, ref.indexID)
			}
			if generation, retiring := s.retiringIndexes[ref.indexID]; retiring {
				if generation != s.indexGeneration[ref.indexID] || normalizedEngine(s.indexEngine[ref.indexID]) == ref.engine {
					delete(s.retiringIndexes, ref.indexID)
				}
			}
			delete(s.cleanupCandidates, ref)
			continue
		}
		candidate, exists := s.cleanupCandidates[ref]
		if !exists {
			candidate = retiredIndexCandidate{firstSeen: now, lastSeen: now, generation: s.indexGeneration[ref.indexID]}
			s.cleanupCandidates[ref] = candidate
			log.Printf("storage view cleanup candidate discovered engine=%s index_id=%s generation=%d first_seen=%s", ref.engine, ref.indexID, candidate.generation, candidate.firstSeen.Format(time.RFC3339Nano))
			continue
		}
		candidate.lastSeen = now
		s.cleanupCandidates[ref] = candidate
		if now.Sub(candidate.firstSeen) >= age {
			due = append(due, ref)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].engine == due[j].engine {
			return due[i].indexID < due[j].indexID
		}
		return due[i].engine < due[j].engine
	})
	return due
}

func (s *Service) removeRetiredIndex(ctx context.Context, ref managedIndexRef, engine viewindex.Engine, metadata MetadataClient) error {
	if engine == nil {
		return fmt.Errorf("remove retired view index engine=%s index_id=%s: engine unavailable", ref.engine, ref.indexID)
	}

	var runtime *viewRuntime
	if parsed, err := viewindex.ParseViewIndexID(ref.indexID); err == nil {
		s.mu.RLock()
		runtime = s.views[viewRef{spaceID: parsed.SpaceID, viewID: parsed.ViewID}]
		s.mu.RUnlock()
	}
	if runtime != nil {
		if !runtime.mu.TryLock() {
			return fmt.Errorf("remove retired view index engine=%s index_id=%s: runtime is busy", ref.engine, ref.indexID)
		}
	}
	release, err := s.indexWriteGate(ref.indexID).lock(ctx)
	if err != nil {
		if runtime != nil {
			runtime.mu.Unlock()
		}
		return fmt.Errorf("lock retired view index engine=%s index_id=%s: %w", ref.engine, ref.indexID, err)
	}
	defer release()
	if generation, preparing := s.preparingGeneration(ref.indexID); preparing {
		if runtime != nil {
			runtime.mu.Unlock()
		}
		s.cancelCleanupCandidate(ref, "preparing")
		log.Printf("storage view cleanup candidate deferred engine=%s index_id=%s generation=%d reason=preparing", ref.engine, ref.indexID, generation)
		return nil
	}
	if runtime != nil {
		if (runtime.active == ref.indexID || runtime.next == ref.indexID) && s.indexEngineMatches(ref) {
			runtime.mu.Unlock()
			s.cancelCleanupCandidate(ref, "runtime_referenced")
			return nil
		}
		runtime.mu.Unlock()
	}

	// Re-read Metadata while holding the per-index gate. This closes the race
	// where a build is attached after the run-wide snapshot but before remove.
	protected, err := metadataProtectedIndexes(ctx, metadata, s.internalAuth())
	if err != nil {
		s.cancelCleanupCandidate(ref, "metadata_revalidation_failed")
		return fmt.Errorf("revalidate retired view index engine=%s index_id=%s: %w", ref.engine, ref.indexID, err)
	}
	if cleanupIndexProtected(protected, ref) {
		s.cancelCleanupCandidate(ref, "metadata_referenced")
		return nil
	}

	s.mu.RLock()
	candidate, exists := s.cleanupCandidates[ref]
	currentGeneration := s.indexGeneration[ref.indexID]
	s.mu.RUnlock()
	if !exists {
		return nil
	}
	if currentGeneration != candidate.generation {
		s.cancelCleanupCandidate(ref, "generation_changed")
		return nil
	}
	if err := engine.Remove(ctx, ref.indexID); err != nil {
		log.Printf("storage view cleanup delete failed engine=%s index_id=%s generation=%d age=%s err=%v", ref.engine, ref.indexID, candidate.generation, candidate.lastSeen.Sub(candidate.firstSeen), err)
		return fmt.Errorf("remove retired view index engine=%s index_id=%s: %w", ref.engine, ref.indexID, err)
	}

	s.mu.Lock()
	if current, ok := s.cleanupCandidates[ref]; ok && current.generation == candidate.generation {
		delete(s.cleanupCandidates, ref)
	}
	mappedEngine := normalizedEngine(s.indexEngine[ref.indexID])
	if generation, ok := s.retiringIndexes[ref.indexID]; ok && generation == candidate.generation && (mappedEngine == "" || mappedEngine == ref.engine) {
		delete(s.retiringIndexes, ref.indexID)
	}
	if s.indexGeneration[ref.indexID] == candidate.generation && mappedEngine == ref.engine {
		s.removeIndexMappingsLocked(ref.indexID)
		delete(s.indexEngine, ref.indexID)
		delete(s.schemas, ref.indexID)
		delete(s.indexView, ref.indexID)
	}
	s.mu.Unlock()
	log.Printf("storage view cleanup deleted engine=%s index_id=%s generation=%d first_seen=%s", ref.engine, ref.indexID, candidate.generation, candidate.firstSeen.Format(time.RFC3339Nano))
	return nil
}

func (s *Service) indexEngineMatches(ref managedIndexRef) bool {
	s.mu.RLock()
	engine := normalizedEngine(s.indexEngine[ref.indexID])
	s.mu.RUnlock()
	// Missing legacy mappings are fail-closed while a runtime still references
	// the ID. A known different engine must not protect a stale sibling artifact.
	return engine == "" || engine == ref.engine
}

func (s *Service) cancelCleanupCandidate(ref managedIndexRef, reason string) {
	s.mu.Lock()
	candidate, ok := s.cleanupCandidates[ref]
	if ok {
		delete(s.cleanupCandidates, ref)
		if generation, retiring := s.retiringIndexes[ref.indexID]; retiring && generation == candidate.generation {
			if generation != s.indexGeneration[ref.indexID] || normalizedEngine(s.indexEngine[ref.indexID]) == ref.engine {
				delete(s.retiringIndexes, ref.indexID)
			}
		}
	}
	s.mu.Unlock()
	if ok {
		log.Printf("storage view cleanup candidate cancelled engine=%s index_id=%s generation=%d reason=%s", ref.engine, ref.indexID, candidate.generation, reason)
	}
}

func (s *Service) resetCleanupCandidates() {
	s.mu.Lock()
	s.cleanupCandidates = make(map[managedIndexRef]retiredIndexCandidate)
	s.mu.Unlock()
}

func (s *Service) resetCleanupCandidatesForEngine(engine string) {
	s.mu.Lock()
	for ref := range s.cleanupCandidates {
		if ref.engine == engine {
			delete(s.cleanupCandidates, ref)
		}
	}
	s.mu.Unlock()
}
