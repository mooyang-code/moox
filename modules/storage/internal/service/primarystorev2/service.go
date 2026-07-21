package primarystorev2

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

type Validator interface {
	ValidateRow(context.Context, *pb.RowFieldUpsert) error
}
type NodeResolver func(context.Context, string, string) (pb.DataNodeService, error)
type ViewResolver func(context.Context, string, string) (pb.DataViewService, string, error)
type AuthSigner func(*pb.AuthInfo) (*pb.AuthInfo, error)
type Authorizer func(*pb.AuthInfo) error
type routeKey struct{ spaceID, datasetID string }

type Service struct {
	resolve   NodeResolver
	validate  Validator
	sign      AuthSigner
	authorize Authorizer
	view      ViewResolver
}

type Options struct {
	Node       pb.DataNodeService
	Resolver   NodeResolver
	Validator  Validator
	AuthSigner AuthSigner
	Authorizer Authorizer
	View       ViewResolver
}

func New(opts Options) (*Service, error) {
	resolve := opts.Resolver
	if resolve == nil && opts.Node != nil {
		resolve = func(context.Context, string, string) (pb.DataNodeService, error) { return opts.Node, nil }
	}
	if resolve == nil {
		return nil, errors.New("primary store node resolver is required")
	}
	return &Service{resolve: resolve, validate: opts.Validator, sign: opts.AuthSigner, authorize: opts.Authorizer, view: opts.View}, nil
}

func (s *Service) WriteFields(ctx context.Context, req *pb.PrimaryWriteFieldsReq) (*pb.PrimaryWriteFieldsRsp, error) {
	if req == nil || len(req.GetRows()) == 0 {
		return &pb.PrimaryWriteFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("rows are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryWriteFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	groups := make(map[routeKey][]*pb.RowFieldUpsert)
	order := make([]routeKey, 0)
	for _, row := range req.GetRows() {
		if err := validateRow(ctx, row, s.validate); err != nil {
			return &pb.PrimaryWriteFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		group := routeKey{spaceID: row.GetKey().GetSpaceId(), datasetID: row.GetKey().GetDatasetId()}
		if _, ok := groups[group]; !ok {
			order = append(order, group)
		}
		groups[group] = append(groups[group], row)
	}
	keys := make([]*pb.RowKey, 0, len(req.GetRows()))
	for _, group := range order {
		rows := groups[group]
		node, err := s.resolve(ctx, group.spaceID, group.datasetID)
		if err != nil {
			return nil, err
		}
		auth, err := s.signAuth(req.GetAuthInfo())
		if err != nil {
			return &pb.PrimaryWriteFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
		}
		rsp, err := node.WriteFields(ctx, &pb.WriteFieldsReq{AuthInfo: auth, Rows: rows})
		if err != nil {
			return nil, err
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return &pb.PrimaryWriteFieldsRsp{RetInfo: rsp.GetRetInfo()}, nil
		}
		keys = append(keys, rsp.GetKeys()...)
	}
	return &pb.PrimaryWriteFieldsRsp{RetInfo: retinfo.Success("success"), Keys: keys}, nil
}

func (s *Service) ReadFields(ctx context.Context, req *pb.PrimaryReadFieldsReq) (*pb.PrimaryReadFieldsRsp, error) {
	if req == nil || len(req.GetKeys()) == 0 {
		return &pb.PrimaryReadFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("keys are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryReadFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	groups := make(map[routeKey][]*pb.RowKey)
	order := make([]routeKey, 0)
	for _, key := range req.GetKeys() {
		if key == nil || key.GetSpaceId() == "" || key.GetDatasetId() == "" {
			return &pb.PrimaryReadFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("row key is invalid"))}, nil
		}
		group := routeKey{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()}
		if _, ok := groups[group]; !ok {
			order = append(order, group)
		}
		groups[group] = append(groups[group], key)
	}
	rowsByKey := make(map[string][]*pb.RowFieldValues, len(req.GetKeys()))
	existingByKey := make(map[string]bool, len(req.GetKeys()))
	existingLatest := make(map[string]bool, len(req.GetKeys()))
	latestRecordRows := make(map[string][]*pb.RowFieldValues)
	for _, group := range order {
		keys := groups[group]
		node, err := s.resolve(ctx, group.spaceID, group.datasetID)
		if err != nil {
			return nil, err
		}
		auth, err := s.signAuth(req.GetAuthInfo())
		if err != nil {
			return &pb.PrimaryReadFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
		}
		rsp, err := node.ReadFields(ctx, &pb.ReadFieldsReq{AuthInfo: auth, DatasetId: group.datasetID, Keys: keys, FieldIds: req.GetFieldIds(), AttributeKeys: req.GetAttributeKeys()})
		if err != nil {
			return nil, err
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return &pb.PrimaryReadFieldsRsp{RetInfo: rsp.GetRetInfo()}, nil
		}
		for _, row := range rsp.GetRows() {
			id := rowKeyIdentity(row.GetKey())
			rowsByKey[id] = append(rowsByKey[id], row)
			if row.GetKey().GetRecord() != nil {
				latestID := latestRecordIdentity(row.GetKey())
				latestRecordRows[latestID] = append(latestRecordRows[latestID], row)
			}
		}
		for _, key := range rsp.GetExistingKeys() {
			existingByKey[rowKeyIdentity(key)] = true
			if key.GetRecord() != nil {
				existingLatest[latestRecordIdentity(key)] = true
			}
		}
	}
	rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		if record := key.GetRecord(); record != nil && record.GetVersion() == "" {
			id := latestRecordIdentity(key)
			matches := latestRecordRows[id]
			if len(matches) != 0 {
				rows = append(rows, matches[0])
				latestRecordRows[id] = matches[1:]
			}
			continue
		}
		id := rowKeyIdentity(key)
		matches := rowsByKey[id]
		if len(matches) == 0 {
			continue
		}
		rows = append(rows, matches[0])
		rowsByKey[id] = matches[1:]
	}
	existing := make([]*pb.RowKey, 0, len(existingByKey))
	for _, key := range req.GetKeys() {
		if existingByKey[rowKeyIdentity(key)] || (key.GetRecord() != nil && existingLatest[latestRecordIdentity(key)]) {
			existing = append(existing, key)
		}
	}
	return &pb.PrimaryReadFieldsRsp{RetInfo: retinfo.Success("success"), Rows: rows, ExistingKeys: existing}, nil
}

func latestRecordIdentity(key *pb.RowKey) string {
	if key == nil || key.GetRecord() == nil {
		return ""
	}
	return key.GetSpaceId() + "\x00" + key.GetDatasetId() + "\x00" + key.GetRecord().GetRecordId()
}

func (s *Service) authorizeRequest(auth *pb.AuthInfo) error {
	if auth == nil {
		return errors.New("auth_info is required")
	}
	if s.authorize != nil {
		return s.authorize(auth)
	}
	return nil
}

func validateRow(ctx context.Context, row *pb.RowFieldUpsert, validator Validator) error {
	if row == nil || row.GetKey() == nil {
		return errors.New("row key is required")
	}
	key := row.GetKey()
	if strings.TrimSpace(key.GetSpaceId()) == "" || strings.TrimSpace(key.GetDatasetId()) == "" {
		return errors.New("space_id and dataset_id are required")
	}
	if key.GetTimeSeries() == nil && key.GetRecord() == nil {
		return errors.New("row key kind is required")
	}
	if record := key.GetRecord(); record != nil && strings.TrimSpace(record.GetVersion()) == "" {
		return errors.New("record version is required for writes")
	}
	if len(row.GetFields()) == 0 && len(row.GetAttributes()) == 0 {
		return errors.New("at least one field or attribute is required")
	}
	for _, field := range row.GetFields() {
		if field == nil || strings.TrimSpace(field.GetFieldId()) == "" || field.GetValue() == nil {
			return errors.New("field_id and value are required")
		}
	}
	for name, value := range row.GetAttributes() {
		if strings.TrimSpace(name) == "" || value == nil {
			return errors.New("attribute key and value are required")
		}
	}
	if validator != nil {
		return validator.ValidateRow(ctx, row)
	}
	return nil
}

func (s *Service) signAuth(auth *pb.AuthInfo) (*pb.AuthInfo, error) {
	if auth == nil {
		return nil, errors.New("auth_info is required")
	}
	if s.sign == nil {
		return auth, nil
	}
	return s.sign(auth)
}

func rowKeyIdentity(key *pb.RowKey) string {
	raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(key)
	return string(raw)
}

var _ pb.PrimaryStoreService = (*Service)(nil)

func (s *Service) String() string { return fmt.Sprintf("PrimaryStore[%p]", s) }
