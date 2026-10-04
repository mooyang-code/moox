package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	core "github.com/mooyang-code/moox/packages/doctor"
	"google.golang.org/protobuf/proto"
)

const (
	storageDatasetActivationCheckID = "bootstrap.storage_dataset_activation"
	storageActivationCheckTimeout   = 3 * time.Second
	storageActivationPageSize       = 1000
	storageActivationMaxPages       = 1024
)

// StorageActivationClient is deliberately read-only. Deployment owns the
// separate ActivateDataset state transition.
type StorageActivationClient interface {
	ListDatasets(context.Context, *pb.ListDatasetsReq) (*pb.ListDatasetsRsp, error)
	CheckDatasetActivation(context.Context, *pb.CheckDatasetActivationReq) (*pb.CheckDatasetActivationRsp, error)
}

type storageActivationDataset struct {
	dataset *pb.Dataset
	check   *pb.CheckDatasetActivationRsp
	err     error
}

func (r *bootstrapRunner) runStorageDatasetActivation(ctx context.Context, spec core.CheckSpec) core.CheckResult {
	result := core.CheckResult{ID: spec.ID, Status: core.StatusUnknown}
	datasets, err := listDisabledDatasets(ctx, r.options.StorageActivation)
	if err != nil {
		result.Summary = "Storage Metadata is unavailable for Dataset activation observations"
		result.Error = "storage metadata observation failed"
		return result
	}

	now := bootstrapNow(r.options.Now)
	if len(datasets) == 0 {
		result.Status = core.StatusPass
		result.Summary = "all disabled Datasets passed activation readiness; no disabled Datasets were found"
		return result
	}

	observations := make([]core.Observation, 0, len(datasets))
	status := core.StatusPass
	readyCount := 0
	failedCount := 0
	unknownCount := 0
	for _, dataset := range datasets {
		item := storageActivationDataset{dataset: dataset}
		item.check, item.err = r.options.StorageActivation.CheckDatasetActivation(ctx, &pb.CheckDatasetActivationReq{
			SpaceId:   dataset.GetSpaceId(),
			DatasetId: dataset.GetDatasetId(),
		})
		if item.err != nil {
			unknownCount++
			status = core.StatusUnknown
		} else if item.check == nil {
			unknownCount++
			status = core.StatusUnknown
		} else if item.check.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			failedCount++
			if status != core.StatusUnknown {
				status = core.StatusWarn
			}
		} else if !item.check.GetReady() {
			failedCount++
			if status != core.StatusUnknown {
				status = core.StatusWarn
			}
		} else {
			readyCount++
		}
		observations = append(observations, storageActivationObservation(item, now))
	}

	result.Status = status
	switch {
	case status == core.StatusUnknown:
		result.Summary = fmt.Sprintf("%d disabled Dataset activation observations are unavailable", unknownCount)
	case failedCount > 0:
		result.Summary = fmt.Sprintf("%d of %d disabled Datasets failed activation readiness checks", failedCount, len(datasets))
	default:
		result.Summary = fmt.Sprintf("all %d disabled Datasets passed activation readiness", readyCount)
	}
	result.Observations = boundStorageActivationObservations(observations, now)
	return result
}

func listDisabledDatasets(ctx context.Context, client StorageActivationClient) ([]*pb.Dataset, error) {
	if client == nil {
		return nil, errors.New("storage metadata client is unavailable")
	}
	all := make([]*pb.Dataset, 0)
	for page := uint32(1); page <= storageActivationMaxPages; page++ {
		rsp, err := client.ListDatasets(ctx, &pb.ListDatasetsReq{Page: &pb.Page{Page: page, Size: storageActivationPageSize}})
		if err != nil {
			return nil, err
		}
		if rsp == nil {
			return nil, errors.New("storage metadata returned an empty Dataset list response")
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return nil, errors.New("storage metadata rejected the Dataset list request")
		}
		for _, dataset := range rsp.GetDatasets() {
			if dataset != nil && dataset.GetStatus() == "disabled" {
				all = append(all, dataset)
			}
		}
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
		if page == storageActivationMaxPages {
			return nil, errors.New("storage metadata Dataset list exceeds observation page limit")
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		left, right := all[i], all[j]
		if left.GetSpaceId() != right.GetSpaceId() {
			return left.GetSpaceId() < right.GetSpaceId()
		}
		if left.GetDatasetId() != right.GetDatasetId() {
			return left.GetDatasetId() < right.GetDatasetId()
		}
		return left.GetName() < right.GetName()
	})
	return all, nil
}

func storageActivationObservation(item storageActivationDataset, observedAt time.Time) core.Observation {
	dataset := item.dataset
	observation := core.Observation{
		Source:     "storage_metadata",
		ObservedAt: observedAt,
		ExpiresAt:  observedAt.Add(storageActivationCheckTimeout),
	}
	if item.err != nil || item.check == nil {
		observation.Summary = fmt.Sprintf("Dataset %s/%s activation readiness is unavailable", dataset.GetSpaceId(), dataset.GetDatasetId())
		observation.Error = "metadata activation check unavailable"
		return observation
	}
	observation.Digest = digestStorageActivationResponse(item.check)
	if item.check.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		observation.Summary = fmt.Sprintf("Dataset %s/%s activation check returned a non-success result", dataset.GetSpaceId(), dataset.GetDatasetId())
		observation.Error = "metadata activation check failed"
		return observation
	}
	if item.check.GetReady() {
		observation.Summary = fmt.Sprintf("Dataset %s/%s is ready for activation at revision %d", dataset.GetSpaceId(), dataset.GetDatasetId(), item.check.GetDatasetRevision())
		return observation
	}
	failedChecks := 0
	for _, check := range item.check.GetChecks() {
		if check != nil && !check.GetReady() {
			failedChecks++
		}
	}
	observation.Summary = fmt.Sprintf("Dataset %s/%s is not ready for activation at revision %d (%d checks failed)", dataset.GetSpaceId(), dataset.GetDatasetId(), item.check.GetDatasetRevision(), failedChecks)
	return observation
}

func boundStorageActivationObservations(observations []core.Observation, observedAt time.Time) []core.Observation {
	if len(observations) <= core.MaxObservationsPerCheck {
		return observations
	}
	const omittedObservation = core.MaxObservationsPerCheck - 1
	omitted := len(observations) - omittedObservation
	result := append([]core.Observation(nil), observations[:omittedObservation]...)
	result = append(result, core.Observation{
		Source:     "storage_metadata",
		ObservedAt: observedAt,
		Summary:    fmt.Sprintf("%d disabled Dataset observations omitted from the bounded Doctor report", omitted),
	})
	return result
}

func digestStorageActivationResponse(rsp *pb.CheckDatasetActivationRsp) string {
	if rsp == nil {
		return ""
	}
	// The report carries only this digest, never the RPC payload or its raw
	// summaries. Deterministic protobuf encoding keeps repeated observations
	// stable when the service returns the same result.
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(rsp)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func bootstrapNow(now func() time.Time) time.Time {
	if now != nil {
		return now().UTC()
	}
	return time.Now().UTC()
}
