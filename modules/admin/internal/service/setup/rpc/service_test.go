package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/setup"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

type fakeSetupService struct {
	applyResult setup.Result
	applyErr    error
	status      setup.Status
	statusErr   error
	manifest    setup.Manifest
	backup      setup.Backup
	backupErr   error
}

func (f *fakeSetupService) PutBackup(_ context.Context, envelope string) (time.Time, error) {
	if f.backupErr != nil {
		return time.Time{}, f.backupErr
	}
	f.backup = setup.Backup{Envelope: envelope, UpdatedAt: time.Unix(1700000000, 0)}
	return f.backup.UpdatedAt, nil
}

func (f *fakeSetupService) GetBackup(context.Context) (setup.Backup, error) {
	return f.backup, f.backupErr
}

func (f *fakeSetupService) Apply(_ context.Context, manifest setup.Manifest) (setup.Result, error) {
	f.manifest = manifest
	return f.applyResult, f.applyErr
}

func (f *fakeSetupService) Inspect(_ context.Context, manifest setup.Manifest) (setup.Status, error) {
	f.manifest = manifest
	return f.status, f.statusErr
}

func setupRequest() *pb.ApplySetupReq {
	return &pb.ApplySetupReq{
		Admin:        &pb.SetupAdmin{Username: "admin", Password: "recognizable-admin-password"},
		TencentCloud: &pb.SetupTencentCloud{SecretId: "recognizable-secret-id", SecretKey: "recognizable-secret-key"},
		ControlHost:  &pb.SetupHost{Name: "control", Address: "192.0.2.10", Port: 22, Username: "ubuntu", Password: "recognizable-control-password"},
		OtherHosts:   []*pb.SetupHost{{Name: "compute", Address: "192.0.2.11", Port: 22, Username: "ubuntu", Password: "recognizable-compute-password"}},
		Spaces: []*pb.SetupSpace{{
			SpaceId: "stockcn", Name: "A股市场", Description: "A股行情",
			Owner: "quant", Market: "CN", Timezone: "Asia/Shanghai",
			Status: "active", AttributesJson: `{"managed_by":"moox-cli"}`,
		}},
	}
}

func statusRequest() *pb.GetSetupStatusReq {
	request := setupRequest()
	return &pb.GetSetupStatusReq{
		Admin: request.Admin, TencentCloud: request.TencentCloud,
		ControlHost: request.ControlHost, OtherHosts: request.OtherHosts, Spaces: request.Spaces,
	}
}

func TestApplySetupMapsRequestAndSanitizesResponse(t *testing.T) {
	fake := &fakeSetupService{applyResult: setup.Result{
		Action: "created", Users: 1, Secrets: 1, Hosts: 2,
		Spaces: 1, SpacesCreated: 1,
	}}
	response, err := NewService(fake).ApplySetup(context.Background(), setupRequest())
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
	assert.Equal(t, "created", response.GetAction())
	assert.Equal(t, "admin", fake.manifest.Admin.Username)
	assert.Equal(t, "recognizable-secret-key", fake.manifest.TencentCloud.SecretKey)
	require.Len(t, fake.manifest.Spaces, 1)
	assert.Equal(t, "CN", fake.manifest.Spaces[0].Market)
	assert.Equal(t, "Asia/Shanghai", fake.manifest.Spaces[0].Timezone)
	assert.Equal(t, `{"managed_by":"moox-cli"}`, fake.manifest.Spaces[0].AttributesJSON)
	assert.Equal(t, int32(1), response.GetSpaces())
	assert.Equal(t, int32(1), response.GetSpacesCreated())
	assert.Zero(t, response.GetSpacesUnchanged())

	raw, err := protojson.Marshal(response)
	require.NoError(t, err)
	for _, secret := range []string{"recognizable-admin-password", "recognizable-secret-id", "recognizable-secret-key", "recognizable-control-password", "recognizable-compute-password"} {
		assert.NotContains(t, string(raw), secret)
	}
}

func TestApplySetupMapsDomainErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		code pb.ErrorCode
		msg  string
	}{
		{name: "conflict", err: setup.ErrConflict, code: pb.ErrorCode_INVALID_PARAM, msg: "setup_conflict"},
		{name: "invalid", err: setup.ErrInvalid, code: pb.ErrorCode_INVALID_PARAM, msg: "setup_invalid"},
		{name: "storage", err: setup.ErrStorage, code: pb.ErrorCode_INNER_ERR, msg: "setup_storage_failed"},
		{name: "unknown", err: errors.New("recognizable-secret-key"), code: pb.ErrorCode_INNER_ERR, msg: "setup_storage_failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response, err := NewService(&fakeSetupService{applyErr: tt.err}).ApplySetup(context.Background(), setupRequest())
			require.NoError(t, err)
			assert.Equal(t, tt.code, response.GetRetInfo().GetCode())
			assert.Equal(t, tt.msg, response.GetRetInfo().GetMsg())
			assert.NotContains(t, response.GetRetInfo().GetMsg(), "recognizable-secret-key")
		})
	}
}

func TestGetSetupStatusReturnsRecordDerivedState(t *testing.T) {
	fake := &fakeSetupService{status: setup.Status{State: "incomplete", Users: 1, Secrets: 1, Hosts: 2, Spaces: 1, Missing: 1}}
	response, err := NewService(fake).GetSetupStatus(context.Background(), statusRequest())
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
	assert.Equal(t, "incomplete", response.GetState())
	assert.Equal(t, int32(1), response.GetMissing())
	assert.Equal(t, int32(1), response.GetSpaces())
	assert.Equal(t, "recognizable-admin-password", fake.manifest.Admin.Password)

	raw, err := protojson.Marshal(response)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "recognizable-admin-password")
	assert.NotContains(t, string(raw), "recognizable-secret-key")
}

func TestApplySetupRejectsMissingSections(t *testing.T) {
	response, err := NewService(&fakeSetupService{}).ApplySetup(context.Background(), &pb.ApplySetupReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, response.GetRetInfo().GetCode())
}

func TestConfigBackupRPCs(t *testing.T) {
	fake := &fakeSetupService{}
	service := NewService(fake)
	put, err := service.PutConfigBackup(context.Background(), &pb.PutConfigBackupReq{Envelope: "MOOX-CONFIG-BACKUP v1\nx"})
	if err != nil || put.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || put.GetUpdatedAt() != 1700000000 {
		t.Fatalf("保存: %+v %v", put, err)
	}
	got, err := service.GetConfigBackup(context.Background(), &pb.GetConfigBackupReq{})
	if err != nil || got.GetEnvelope() != "MOOX-CONFIG-BACKUP v1\nx" {
		t.Fatalf("取回: %+v %v", got, err)
	}
	fake.backupErr = setup.ErrBackupMissing
	got, _ = service.GetConfigBackup(context.Background(), &pb.GetConfigBackupReq{})
	if got.GetRetInfo().GetCode() != pb.ErrorCode_NOT_FOUND || got.GetRetInfo().GetMsg() != "config_backup_missing" {
		t.Fatalf("没有备份时应返回 NOT_FOUND: %+v", got.GetRetInfo())
	}
	fake.backupErr = setup.ErrBackupInvalid
	put, _ = service.PutConfigBackup(context.Background(), &pb.PutConfigBackupReq{Envelope: "plain"})
	if put.GetRetInfo().GetMsg() != "config_backup_invalid" {
		t.Fatalf("非法信封: %+v", put.GetRetInfo())
	}
}
