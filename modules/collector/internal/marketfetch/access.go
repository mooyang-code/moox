package marketfetch

import (
	"os"

	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// SCF credentials are supplied by the trusted publisher. No internal gateway
// credential or endpoint is accepted by this external composition root.
func newSCFAccessClient() (*gatewayclient.Client, error) {
	values := map[string]string{}
	for _, key := range []string{"MOOX_ACCESS_ADDRESS", "MOOX_ACCESS_ID", "MOOX_CALLER", "MOOX_CALLER_KEY_ID", "MOOX_CALLER_KEY"} {
		values[key] = os.Getenv(key)
	}
	if err := tencent.ValidateCollectorAccessEnvironment(values); err != nil {
		return nil, err
	}
	return gatewayclient.New(gatewayclient.Config{
		Mode:          gatewayclient.External,
		AccessAddress: os.Getenv("MOOX_ACCESS_ADDRESS"), AccessInstanceID: os.Getenv("MOOX_ACCESS_ID"),
		Credentials: gatewayauth.Credentials{Caller: "scf-collector", KeyID: os.Getenv("MOOX_CALLER_KEY_ID"), Secret: os.Getenv("MOOX_CALLER_KEY")},
	})
}
