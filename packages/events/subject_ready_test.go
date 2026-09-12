package events

import (
	"testing"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSubjectReadinessRequiresComparableSourcePosition(t *testing.T) {
	message := &eventpb.EventMessage{SubjectId: "view"}
	payload := &storagepb.ViewSourceSubjectReady{SourceViewId: "view", SourceDatasetId: "dataset", SubjectId: "BTC", Frequency: "1m", PeriodTime: 100, ActiveIndexId: "index", InputContractVersion: "contract", SourceEventId: "event", ReadyAt: timestamppb.Now()}
	require.ErrorContains(t, validateViewSourceSubjectReady(message, payload), "source position")
	payload.SourceNodeId = "node"
	require.ErrorContains(t, validateViewSourceSubjectReady(message, payload), "source position")
	payload.SourceSequence = 1
	payload.SourceStoreId = "store"
	require.NoError(t, validateViewSourceSubjectReady(message, payload))
}
