package marketfetch

import (
	"os"

	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// 函数的签名身份由 Collector 发布函数时写进环境：scf-collector 的 ed25519 私钥。函数只经外部入口访问系统，
// 不接受内部网关的地址或主机密钥。
func newSCFEntryClient() (*gatewayclient.Client, error) {
	values := map[string]string{}
	for _, key := range []string{"MOOX_ENTRY_ADDRESS", "MOOX_ENTRY_HOST", "MOOX_CALLER", "MOOX_CALLER_KEY_ID", "MOOX_CALLER_KEY"} {
		values[key] = os.Getenv(key)
	}
	if err := tencent.ValidateCollectorEntryEnvironment(values); err != nil {
		return nil, err
	}
	key, err := gatewayauth.ParseSigningKey(values["MOOX_CALLER_KEY"])
	if err != nil {
		return nil, err
	}
	return gatewayclient.New(gatewayclient.Config{
		Mode:         gatewayclient.External,
		EntryAddress: os.Getenv("MOOX_ENTRY_ADDRESS"), EntryHost: os.Getenv("MOOX_ENTRY_HOST"),
		Credentials: gatewayauth.Credentials{Caller: "scf-collector", Key: key},
	})
}
