package storageio

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/providers"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

type metadataSubjectClient interface {
	BatchRegisterDataSubjects(context.Context, *storagepb.BatchRegisterDataSubjectsReq, ...client.Option) (*storagepb.BatchRegisterDataSubjectsRsp, error)
}
type InstrumentMetadataRegistrar struct {
	client                          metadataSubjectClient
	auth                            *storagepb.AuthInfo
	SpaceID, DataSourceID, Timezone string
	DatasetIDs                      []string
}

func NewInstrumentMetadataRegistrar(client metadataSubjectClient, auth *storagepb.AuthInfo, spaceID, dataSourceID, timezone string, datasetIDs []string) *InstrumentMetadataRegistrar {
	return &InstrumentMetadataRegistrar{client: client, auth: auth, SpaceID: spaceID, DataSourceID: dataSourceID, Timezone: timezone, DatasetIDs: append([]string(nil), datasetIDs...)}
}
func (r *InstrumentMetadataRegistrar) RegisterInstruments(ctx context.Context, values []providers.ResolvedInstrument) error {
	if r == nil || r.client == nil || r.SpaceID == "" || r.DataSourceID == "" || len(r.DatasetIDs) == 0 {
		return fmt.Errorf("instrument metadata registrar is not configured")
	}
	items := make([]*storagepb.RegisterDataSubjectReq, 0, len(values))
	for _, value := range values {
		bindings := make([]*storagepb.DatasetSubject, 0, len(r.DatasetIDs))
		for _, datasetID := range r.DatasetIDs {
			bindings = append(bindings, &storagepb.DatasetSubject{DatasetId: datasetID, SubjectRole: "normal", Status: "active"})
		}
		subjectType := string(value.InstrumentType)
		if subjectType == "spot" || subjectType == "swap" {
			subjectType = "crypto_pair"
		}
		items = append(items, &storagepb.RegisterDataSubjectReq{SpaceId: r.SpaceID, DataSourceId: r.DataSourceID, ExternalSymbol: value.ProviderSymbol, Subject: &storagepb.Subject{SubjectId: value.SubjectID, SubjectType: subjectType, Name: value.Name, Market: r.SpaceID, Currency: value.Currency, Timezone: r.Timezone, Status: normalizeInstrumentStatus(value.Status), Attributes: map[string]string{"exchange_id": string(value.ExchangeID), "product_type": string(value.ProductType), "instrument_type": string(value.InstrumentType)}}, DatasetBindings: bindings})
	}
	rsp, err := r.client.BatchRegisterDataSubjects(ctx, &storagepb.BatchRegisterDataSubjectsReq{AuthInfo: r.auth, Items: items})
	if err != nil {
		return err
	}
	return ensureOK("register instrument metadata", rsp.GetRetInfo())
}
func normalizeInstrumentStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "trading", "live", "active":
		return "active"
	case "break", "suspend", "suspended":
		return "suspended"
	default:
		return "inactive"
	}
}
