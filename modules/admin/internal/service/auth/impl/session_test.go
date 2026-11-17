package impl

import (
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go"
)

func TestLogoutDeletesOnlyCurrentSession(t *testing.T) {
	f := newLoginFixture(t)
	current, err := f.svc.sessions.Create("user-login-1", "admin")
	require.NoError(t, err)
	other, err := f.svc.sessions.Create("user-login-1", "admin")
	require.NoError(t, err)
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, model.CtxUserID, []byte("user-login-1"))
	trpc.SetMetaData(ctx, model.CtxSessionID, []byte(current.ID))
	rsp, err := f.svc.Logout(ctx, &pb.LogoutReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	_, ok := f.svc.sessions.Get(current.ID)
	assert.False(t, ok)
	_, ok = f.svc.sessions.Get(other.ID)
	assert.True(t, ok)
}

func TestLogoutRejectsMissingSession(t *testing.T) {
	f := newLoginFixture(t)
	victim, err := f.svc.sessions.Create("user-login-1", "admin")
	require.NoError(t, err)
	rsp, err := f.svc.Logout(trpc.BackgroundContext(), &pb.LogoutReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NO_AUTH, rsp.GetRetInfo().GetCode())
	_, ok := f.svc.sessions.Get(victim.ID)
	assert.True(t, ok)
}
