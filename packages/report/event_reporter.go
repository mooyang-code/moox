package report

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/observabilitypb"
)

type EventReporter struct {
	Registry  *events.Registry
	Publisher Publisher
}

func (r *EventReporter) ReportHealth(ctx context.Context, report *observabilitypb.HealthCheckReport, spaceID string) error {
	if r == nil || r.Registry == nil {
		return fmt.Errorf("health event reporter registry is not initialized")
	}
	if r.Publisher == nil {
		return fmt.Errorf("health event reporter publisher is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if report == nil {
		return fmt.Errorf("health check report is nil")
	}
	observerID := strings.TrimSpace(report.GetObserverId())
	checkID := strings.TrimSpace(report.GetCheckId())
	if observerID != report.GetObserverId() || checkID != report.GetCheckId() {
		return fmt.Errorf("health check observer_id and check_id must not contain surrounding whitespace")
	}
	subjectID := observerID + "/" + checkID
	checkedAt := report.GetCheckedAt()
	if checkedAt == nil {
		return fmt.Errorf("health check checked_at is required")
	}
	occurredAt := checkedAt.AsTime().UTC()
	options := events.PublishOptions{
		EventID:    healthEventID(observerID, checkID, occurredAt),
		OccurredAt: occurredAt,
		SpaceID:    strings.TrimSpace(spaceID),
		SubjectID:  subjectID,
	}
	if _, err := r.Registry.Encode(events.ObservabilityHealthCheckReported, report, options); err != nil {
		return fmt.Errorf("validate health check report: %w", err)
	}
	if _, err := r.Publisher.Publish(ctx, events.ObservabilityHealthCheckReported, report, options); err != nil {
		// Transport implementations may include credential material in their
		// errors. Keep this boundary useful to callers without relaying secrets.
		return fmt.Errorf("publish health check report: eventbus publish failed")
	}
	return nil
}

func healthEventID(observerID, checkID string, checkedAt time.Time) string {
	return fmt.Sprintf("%s-%s-%d", observerID, checkID, checkedAt.UnixNano())
}
