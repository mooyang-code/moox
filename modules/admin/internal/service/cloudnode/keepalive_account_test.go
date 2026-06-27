package cloudnode

import (
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	apperrors "github.com/mooyang-code/moox/modules/admin/internal/errors"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/provider"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"gorm.io/gorm"
)

func TestRunKeepaliveProbeMarksSuccessfulSCFEventNodeOnline(t *testing.T) {
	ctx := context.Background()
	db := newCloudNodeServiceTestDB(t)
	service := newCloudNodeServiceForTest(db)
	fakeClient := &fakeProviderClient{
		invokeFn: func(ctx context.Context, req *provider.InvokeFunctionRequest) (*provider.InvokeFunctionResponse, error) {
			if req.FunctionName != "scf-event-node" {
				return nil, fmt.Errorf("unexpected function name: %s", req.FunctionName)
			}
			return &provider.InvokeFunctionResponse{StatusCode: http.StatusOK, ReturnResult: `{"success":true}`}, nil
		},
	}
	registerFakeTencentProvider(t, fakeClient)

	requireNoError(t, service.accountDAO.CreateCloudAccount(ctx, &model.CloudAccount{
		AccountID:   "acct-keepalive",
		AccountName: "keepalive account",
		Provider:    model.CloudProviderTencent,
		SecretID:    "sid",
		SecretKey:   "skey",
	}))
	requireNoError(t, service.nodeDAO.CreateCloudNode(ctx, &model.CloudNode{
		NodeID:         "scf-event-node",
		CloudAccountID: "acct-keepalive",
		NodeType:       model.NodeTypeSCFEvent,
		BizType:        model.PackageTypeDataCollector,
		Namespace:      "default",
		Region:         "ap-guangzhou",
		ProbeEnabled:   true,
	}))

	requireNoError(t, service.RunKeepaliveProbe(ctx))

	if fakeClient.invokeCount != 1 {
		t.Fatalf("expected one cloud function invoke, got %d", fakeClient.invokeCount)
	}
	heartbeat := service.heartbeatStore.GetHeartbeat("scf-event-node")
	if heartbeat == nil {
		t.Fatalf("expected keepalive success to update heartbeat store")
	}
	if heartbeat.SourceService != keepaliveSource {
		t.Fatalf("expected source service %q, got %q", keepaliveSource, heartbeat.SourceService)
	}

	resp, err := service.GetNodeList(ctx, &pb.GetNodeListReq{Query: &pb.NodeListRequest{Page: 1, PageSize: 20}})
	requireNoError(t, err)
	if len(resp.Items) != 1 {
		t.Fatalf("expected one node, got %d", len(resp.Items))
	}
	if resp.Items[0].GetLastHeartbeat() == "" {
		t.Fatalf("expected node list item to include last heartbeat")
	}
	if resp.Items[0].GetStatus() != "online" {
		t.Fatalf("expected node status online, got %q", resp.Items[0].GetStatus())
	}
}

func TestDeleteAccountRejectsAccountReferencedByActiveNode(t *testing.T) {
	ctx := context.Background()
	db := newCloudNodeServiceTestDB(t)
	service := newCloudNodeServiceForTest(db)

	requireNoError(t, service.accountDAO.CreateCloudAccount(ctx, &model.CloudAccount{
		AccountID:   "acct-node-ref",
		AccountName: "node ref account",
		Provider:    model.CloudProviderTencent,
		SecretID:    "sid",
		SecretKey:   "skey",
	}))
	requireNoError(t, service.nodeDAO.CreateCloudNode(ctx, &model.CloudNode{
		NodeID:         "referencing-node",
		CloudAccountID: "acct-node-ref",
		NodeType:       model.NodeTypeSCFEvent,
		Namespace:      "default",
		Region:         "ap-guangzhou",
	}))

	err := service.DeleteAccount(ctx, "acct-node-ref")
	if err == nil {
		t.Fatalf("expected delete to be rejected when active node references account")
	}
	assertConflictError(t, err, "1 个节点")

	account, err := service.accountDAO.GetCloudAccount(ctx, "acct-node-ref")
	requireNoError(t, err)
	if account.Invalid != model.InvalidNo {
		t.Fatalf("expected account to remain valid after rejected delete, invalid=%d", account.Invalid)
	}
}

func TestDeleteAccountRejectsAccountReferencedByActivePackage(t *testing.T) {
	ctx := context.Background()
	db := newCloudNodeServiceTestDB(t)
	service := newCloudNodeServiceForTest(db)

	requireNoError(t, service.accountDAO.CreateCloudAccount(ctx, &model.CloudAccount{
		AccountID:   "acct-package-ref",
		AccountName: "package ref account",
		Provider:    model.CloudProviderTencent,
		SecretID:    "sid",
		SecretKey:   "skey",
	}))
	requireNoError(t, service.packageDAO.Create(ctx, &model.FunctionPackage{
		PackageID:        "pkg-ref",
		PackageName:      "DataCollector",
		Version:          "v1",
		Runtime:          model.RuntimeGo1,
		PackageType:      model.PackageTypeDataCollector,
		OriginalFilename: "collector.zip",
		FileSize:         1,
		FileMD5:          "md5",
		CloudAccountID:   "acct-package-ref",
		COSBucket:        "bucket",
		COSPath:          "collector.zip",
		Status:           model.PackageStatusAvailable,
	}))

	err := service.DeleteAccount(ctx, "acct-package-ref")
	if err == nil {
		t.Fatalf("expected delete to be rejected when active package references account")
	}
	assertConflictError(t, err, "1 个代码包")

	account, err := service.accountDAO.GetCloudAccount(ctx, "acct-package-ref")
	requireNoError(t, err)
	if account.Invalid != model.InvalidNo {
		t.Fatalf("expected account to remain valid after rejected delete, invalid=%d", account.Invalid)
	}
}

func newCloudNodeServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	requireNoError(t, err)
	requireNoError(t, db.AutoMigrate(&model.CloudAccount{}, &model.CloudNode{}, &model.FunctionPackage{}))
	return db
}

func newCloudNodeServiceForTest(db *gorm.DB) *ServiceImpl {
	service := &ServiceImpl{
		nodeDAO:        dao.NewCloudNodeDAO(db),
		accountDAO:     dao.NewCloudAccountDAO(db),
		packageDAO:     dao.NewFunctionPackageDAO(db),
		heartbeatStore: NewHeartbeatStore(),
		probeStore:     NewProbeStore(),
	}
	service.init()
	return service
}

func registerFakeTencentProvider(t *testing.T, client provider.Client) {
	t.Helper()

	original, hadOriginal := provider.GetConstructor(provider.Tencent)
	requireNoError(t, provider.RegisterCloudPlatform(provider.Tencent, func(config *provider.Config) (provider.Client, error) {
		return client, nil
	}))
	t.Cleanup(func() {
		if hadOriginal {
			_ = provider.RegisterCloudPlatform(provider.Tencent, original)
		}
	})
}

func assertConflictError(t *testing.T, err error, messagePart string) {
	t.Helper()

	var appErr *apperrors.AppError
	if !stderrors.As(err, &appErr) {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.HTTPStatus != http.StatusConflict {
		t.Fatalf("expected HTTP 409, got %d", appErr.HTTPStatus)
	}
	if !strings.Contains(appErr.Message, messagePart) {
		t.Fatalf("expected message to contain %q, got %q", messagePart, appErr.Message)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

type fakeProviderClient struct {
	invokeFn    func(context.Context, *provider.InvokeFunctionRequest) (*provider.InvokeFunctionResponse, error)
	invokeCount int
}

func (f *fakeProviderClient) CreateFunction(context.Context, *provider.CreateFunctionRequest) (*provider.FunctionInfo, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) UpdateFunction(context.Context, *provider.UpdateFunctionRequest) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) DeleteFunction(context.Context, string, string, string) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) GetFunction(context.Context, string, string, string) (*provider.FunctionInfo, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) ListFunctions(context.Context, string, string) ([]*provider.FunctionInfo, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) CreateNamespace(context.Context, string, string, string) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) DeleteNamespace(context.Context, string, string) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) ListNamespaces(context.Context, string) ([]*provider.NamespaceInfo, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) CreateTrigger(context.Context, *provider.CreateTriggerRequest) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) DeleteTrigger(context.Context, string, string, string, string) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) ListTriggers(context.Context, string, string, string) ([]*provider.TriggerInfo, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) InvokeFunction(ctx context.Context, req *provider.InvokeFunctionRequest) (*provider.InvokeFunctionResponse, error) {
	f.invokeCount++
	if f.invokeFn == nil {
		return &provider.InvokeFunctionResponse{StatusCode: http.StatusOK}, nil
	}
	return f.invokeFn(ctx, req)
}

func (f *fakeProviderClient) UploadCOS(context.Context, *provider.UploadCOSRequest) (*provider.UploadCOSResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) UploadCOSWithReader(context.Context, string, string, io.Reader, string) (*provider.UploadCOSResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) DeleteCOSObject(context.Context, string, string) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) GetCOSObjectURL(context.Context, string, string, time.Duration) (string, error) {
	return "", fmt.Errorf("not implemented")
}

func (f *fakeProviderClient) DownloadCOSToFile(context.Context, string, string) error {
	return fmt.Errorf("not implemented")
}
