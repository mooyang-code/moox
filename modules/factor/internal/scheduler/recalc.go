package scheduler

import "time"

// RecalcRequest describes a manual low-priority recalculation request.
type RecalcRequest struct {
	RecalcID  string
	SpaceID   string
	DatasetID string
	SubjectID string
	Freq      string
	Start     time.Time
	End       time.Time
	FactorIDs []string
}
