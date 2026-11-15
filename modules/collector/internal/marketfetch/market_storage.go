package marketfetch

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// NewMarketStorage borrows the invocation's single Access client.
func NewMarketStorage(gateway gatewayclient.Invoker) (Storage, error) {
	storage, err := marketstorage.NewGatewayBatchStorage(gateway)
	if err != nil {
		return nil, fmt.Errorf("create market storage: %w", err)
	}
	return storage, nil
}
