package jobqueue

import (
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
)

// JobItemMessage is the opaque CloudNode execution queue payload.
type JobItemMessage struct {
	SpaceID       string         `json:"space_id"`
	JobID         string         `json:"job_id"`
	JobItemID     string         `json:"job_item_id"`
	JobType       string         `json:"job_type"`
	CodePackageID string         `json:"code_package_id"`
	Params        map[string]any `json:"params"`
	Priority      int32          `json:"priority"`
	SubmittedAt   time.Time      `json:"submitted_at"`
}

func structToMap(st *structpb.Struct) map[string]any {
	if st == nil {
		return map[string]any{}
	}
	return st.AsMap()
}

func mapToStruct(values map[string]any) *structpb.Struct {
	st, err := structpb.NewStruct(values)
	if err != nil {
		return &structpb.Struct{}
	}
	return st
}

// ToPolledJobItem converts a queue delivery into the RPC shape consumed by SCF runtimes.
func (m JobItemMessage) ToPolledJobItem(attemptNo int) *pb.PolledJobItem {
	return &pb.PolledJobItem{
		SpaceId:       m.SpaceID,
		JobId:         m.JobID,
		JobItemId:     m.JobItemID,
		JobType:       m.JobType,
		CodePackageId: m.CodePackageID,
		Params:        mapToStruct(m.Params),
		AttemptNo:     int32(attemptNo),
	}
}

// Delivery is a fetched queue message plus ack metadata.
type Delivery struct {
	Message     JobItemMessage
	AttemptNo   int
	AckSubject  string
	StreamSeq   uint64
	ConsumerSeq uint64
}
