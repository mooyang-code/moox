package marketwiring

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// 函数写 Storage 只需要网关连接，不读任何存储口令配置。
func TestHandlerStorageFactoryNeedsOnlyGateway(t *testing.T) {
	storage, err := marketfetch.NewMarketStorage(wiringGateway{})
	require.NoError(t, err)
	require.NotNil(t, storage)
	_, err = marketfetch.NewMarketStorage(nil)
	require.Error(t, err)
}

type timerHandlerStorage struct{}

func (timerHandlerStorage) UpsertFields(context.Context, []*storagepb.Row) error {
	return nil
}

type wiringGateway struct{}

func (wiringGateway) Invoke(context.Context, string, string, any, any) error { return nil }
