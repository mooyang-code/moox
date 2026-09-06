package input

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
)

var (
	ErrPoolUDFNotRegistered       = errors.New("pool udf is not registered")
	ErrPoolUDFInvalidParams       = errors.New("pool udf params are invalid")
	ErrPoolUDFRegistryUnavailable = errors.New("pool udf registry is unavailable")
)

// PoolUDF receives only the frozen subject directory and decision period. It
// cannot perform I/O, which keeps pool resolution deterministic and testable.
type PoolUDF func(context.Context, PoolUDFInput) ([]string, error)

type PoolUDFInput struct {
	Subjects   []Subject
	BarEndTime time.Time
	Params     map[string]any
}

type UDFRegistry struct {
	mu    sync.RWMutex
	funcs map[string]registeredPoolUDF
}

type registeredPoolUDF struct {
	fn       PoolUDF
	validate func(map[string]any) error
}

func NewUDFRegistry() *UDFRegistry { return &UDFRegistry{funcs: map[string]registeredPoolUDF{}} }
func (r *UDFRegistry) Register(name string, fn PoolUDF) error {
	return r.RegisterValidated(name, nil, fn)
}

// RegisterValidated adds a UDF and an optional parameter validator. The
// validator runs before an instance is enabled and again at execution time so
// malformed parameters cannot silently widen a live pool.
func (r *UDFRegistry) RegisterValidated(name string, validate func(map[string]any) error, fn PoolUDF) error {
	name = strings.TrimSpace(name)
	if name == "" || fn == nil {
		return errors.New("pool udf name and function are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.funcs[name]; exists {
		return errors.New("pool udf already registered")
	}
	r.funcs[name] = registeredPoolUDF{fn: fn, validate: validate}
	return nil
}

// Has reports whether a pool UDF is registered. It is used by the Strategy
// control plane to reject an instance before it can enter an infinite retry
// loop in the durable ready-event consumer.
func (r *UDFRegistry) Has(name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	_, ok := r.funcs[strings.TrimSpace(name)]
	r.mu.RUnlock()
	return ok
}
func (r *UDFRegistry) Resolve(ctx context.Context, pool config.Pool, subjects []Subject, at time.Time) ([]string, error) {
	if pool.UDF == nil {
		ids := append([]string(nil), pool.Fixed...)
		return normalizePoolIDs(ids, subjects)
	}
	r.mu.RLock()
	entry := r.funcs[pool.UDF.Name]
	r.mu.RUnlock()
	if entry.fn == nil {
		return nil, fmt.Errorf("%w: %s", ErrPoolUDFNotRegistered, pool.UDF.Name)
	}
	if entry.validate != nil {
		if err := entry.validate(pool.UDF.Params); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrPoolUDFInvalidParams, pool.UDF.Name, err)
		}
	}
	ids, err := entry.fn(ctx, PoolUDFInput{Subjects: append([]Subject(nil), subjects...), BarEndTime: at, Params: pool.UDF.Params})
	if err != nil {
		return nil, err
	}
	return normalizePoolIDs(ids, subjects)
}

// Validate checks that a UDF exists and accepts its persisted parameters.
func (r *UDFRegistry) Validate(pool config.Pool) error {
	if pool.UDF == nil {
		return nil
	}
	if r == nil {
		return ErrPoolUDFRegistryUnavailable
	}
	r.mu.RLock()
	entry := r.funcs[strings.TrimSpace(pool.UDF.Name)]
	r.mu.RUnlock()
	if entry.fn == nil {
		return fmt.Errorf("%w: %s", ErrPoolUDFNotRegistered, pool.UDF.Name)
	}
	if entry.validate != nil {
		if err := entry.validate(pool.UDF.Params); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrPoolUDFInvalidParams, pool.UDF.Name, err)
		}
	}
	return nil
}

func normalizePoolIDs(ids []string, subjects []Subject) ([]string, error) {
	known := map[string]string{}
	for _, subject := range subjects {
		id := strings.TrimSpace(subject.InstrumentID)
		key := strings.ToUpper(id)
		if _, exists := known[key]; !exists {
			known[key] = id
		}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, errors.New("pool udf returned empty instrument id")
		}
		key := strings.ToUpper(id)
		canonical, ok := known[key]
		if !ok {
			return nil, errors.New("pool udf returned instrument outside directory: " + id)
		}
		if _, ok := seen[key]; ok {
			return nil, errors.New("pool udf returned duplicate instrument: " + id)
		}
		seen[key] = struct{}{}
		out = append(out, canonical)
	}
	sort.Strings(out)
	return out, nil
}
