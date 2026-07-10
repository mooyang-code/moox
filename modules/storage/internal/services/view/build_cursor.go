package view

import (
	"encoding/json"
	"errors"
)

const (
	buildPhaseBackfill = "backfill"
	buildPhaseCatchUp  = "catch_up"
)

type buildCursor struct {
	Phase             string `json:"phase"`
	Cursor            string `json:"cursor,omitempty"`
	SnapshotID        string `json:"snapshot_id,omitempty"`
	SnapshotCommitSeq uint64 `json:"snapshot_commit_seq,omitempty"`
	SourceID          string `json:"source_id,omitempty"`
	ReplayedCommitSeq uint64 `json:"replayed_commit_seq,omitempty"`
}

func encodeBuildCursor(cursor buildCursor) (string, error) {
	if cursor.Phase != buildPhaseBackfill && cursor.Phase != buildPhaseCatchUp {
		return "", errors.New("invalid View index build cursor phase")
	}
	raw, err := json.Marshal(cursor)
	return string(raw), err
}

func decodeBuildCursor(raw string) (buildCursor, error) {
	if raw == "" {
		return buildCursor{Phase: buildPhaseBackfill}, nil
	}
	var cursor buildCursor
	if err := json.Unmarshal([]byte(raw), &cursor); err != nil {
		return buildCursor{}, err
	}
	if cursor.Phase != buildPhaseBackfill && cursor.Phase != buildPhaseCatchUp {
		return buildCursor{}, errors.New("invalid View index build cursor phase")
	}
	return cursor, nil
}
