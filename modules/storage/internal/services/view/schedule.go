package view

import (
	"context"
	"net/url"
	"strings"
	"sync"

	"trpc.group/trpc-go/trpc-go/log"
)

var defaultRotation struct {
	sync.RWMutex
	value *RotationManager
}

// SetDefaultRotation registers the RotationManager used by HandleSchedule.
// It should only be called by the view_builder role.
func SetDefaultRotation(rotation *RotationManager) {
	defaultRotation.Lock()
	defer defaultRotation.Unlock()
	defaultRotation.value = rotation
}

func currentDefaultRotation() *RotationManager {
	defaultRotation.RLock()
	defer defaultRotation.RUnlock()
	return defaultRotation.value
}

var defaultBuilder struct {
	sync.RWMutex
	value *Builder
}

// SetDefaultBuilder registers the legacy single-view Builder. It is kept
// for callers that still construct a Builder directly; the scheduled View
// index lifecycle now runs exclusively through RotateViewIndexes.
func SetDefaultBuilder(builder *Builder) {
	defaultBuilder.Lock()
	defer defaultBuilder.Unlock()
	defaultBuilder.value = builder
}

// HandleSchedule is the single scheduler entry for the View index
// lifecycle. It only supports op=rotate (default when op is omitted); all
// other schedule ops are unsupported and are skipped with a warning.
func HandleSchedule(ctx context.Context, params string) error {
	op := scheduleOperation(params)
	if op != "" && op != "rotate" {
		log.WarnContextf(ctx, "[ViewBuilder] unsupported schedule op %q, skip", op)
		return nil
	}
	rotation := currentDefaultRotation()
	if rotation == nil {
		log.WarnContext(ctx, "[ViewBuilder] rotation manager is not initialized, skip schedule")
		return nil
	}
	spaceID := scheduleSpaceID(params)
	rotated, err := rotation.RotateViewIndexes(ctx, spaceID)
	if err != nil {
		log.ErrorContextf(ctx, "[ViewBuilder] rotate schedule failed: %v", err)
		return err
	}
	log.InfoContextf(ctx, "[ViewBuilder] rotate schedule updated %d view(s)", rotated)
	return nil
}

func scheduleSpaceID(params string) string {
	params = strings.TrimSpace(params)
	if params == "" {
		return ""
	}
	values, err := url.ParseQuery(strings.TrimPrefix(params, "?"))
	if err == nil {
		if spaceID := strings.TrimSpace(values.Get("space_id")); spaceID != "" {
			return spaceID
		}
	}
	if !strings.Contains(params, "=") {
		return params
	}
	return ""
}

func scheduleOperation(params string) string {
	params = strings.TrimSpace(params)
	if params == "" {
		return ""
	}
	values, err := url.ParseQuery(strings.TrimPrefix(params, "?"))
	if err == nil {
		if op := strings.TrimSpace(values.Get("op")); op != "" {
			return strings.ToLower(op)
		}
		if action := strings.TrimSpace(values.Get("action")); action != "" {
			return strings.ToLower(action)
		}
	}
	return ""
}
