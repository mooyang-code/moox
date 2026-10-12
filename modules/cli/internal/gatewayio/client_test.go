package gatewayio

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
)

type gatewayWire struct {
	pb.UnimplementedPrimaryStore
	directorypb.UnimplementedDirectory
	credentials gatewayauth.Credentials
	mu          sync.Mutex
	host        string
	calls       map[string]int
	nonces      map[string]bool
}

func (w *gatewayWire) GetDirectory(_ context.Context, _ *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	d := servicecatalog.Directory{Services: map[string][]string{"trpc.moox.storage.PrimaryStore": {w.host}}, Hosts: map[string]servicecatalog.DirectoryHost{w.host: {Address: "public-address-must-not-be-dialed.example.test"}}}
	version, err := d.VersionHash()
	return &directorypb.GetDirectoryRsp{Changed: true, Version: version, Services: map[string]*directorypb.ServiceHosts{"trpc.moox.storage.PrimaryStore": {HostIds: []string{w.host}}}, Hosts: map[string]*directorypb.DirectoryHost{w.host: {Address: d.Hosts[w.host].Address}}}, err
}

func (w *gatewayWire) verify(ctx context.Context, method string, request proto.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	headers := http.Header{}
	for key, value := range codec.Message(ctx).ServerMetaData() {
		headers.Set(key, string(value))
	}
	body, err := proto.Marshal(request)
	if err != nil {
		return err
	}
	service := "trpc.moox.storage.PrimaryStore"
	claims, err := gatewayauth.Verify(w.credentials, gatewayauth.Request{Method: "POST", Path: "/" + service + "/" + method, TargetNode: w.host, Callee: service, Func: method, Body: body}, headers, time.Now())
	if err != nil {
		return err
	}
	role := request.(interface{ GetAuthInfo() *pb.AuthInfo }).GetAuthInfo()
	if role.GetAppId() != "storage-role" || role.GetAppKey() != "role-key" || w.nonces[claims.Nonce] {
		return errors.New("Storage role changed or nonce reused")
	}
	w.nonces[claims.Nonce] = true
	w.calls[method]++
	if method == "ReadTimeSeriesRows" && w.calls[method] == 1 {
		w.host = "storage-next"
		return errs.NewFrameError(errs.RetServerNoService, "refresh directory")
	}
	if method == "EnsureDatasetPeriod" {
		return errs.NewFrameError(errs.RetServerNoService, "write must be sent once")
	}
	return nil
}

func (w *gatewayWire) ReadTimeSeriesRows(ctx context.Context, request *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, w.verify(ctx, "ReadTimeSeriesRows", request)
}

func TestOperatorIdentityRejectsUnsafeAndObsoleteConfiguration(t *testing.T) {
	base := "caller: moox-cli\nkey_id: admin-generated-key\nkey_file: caller-moox-cli.key\n"
	for _, raw := range []string{strings.Replace(base, "moox-cli", "collector", 1), strings.Replace(base, "admin-generated-key", "", 1), strings.Replace(base, "admin-generated-key", "invalid/key", 1), base + "target: ip://public.example:11003\n", base + "caller: moox-cli\n", base + "---\n{}\n"} {
		path := filepath.Join(t.TempDir(), "identity.yaml")
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
		_, err := loadIdentity(path)
		require.Error(t, err)
	}
	path := filepath.Join(t.TempDir(), "identity.yaml")
	require.NoError(t, os.WriteFile(path, []byte(base), 0o644))
	_, err := loadIdentity(path)
	require.Error(t, err)
	require.NoError(t, os.Chmod(path, 0o600))
	link := path + ".link"
	require.NoError(t, os.Symlink(path, link))
	_, err = loadIdentity(link)
	require.Error(t, err)
}
