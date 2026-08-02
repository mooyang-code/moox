package main

import (
	"fmt"
	"os"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	cryptomarket "github.com/mooyang-code/moox/modules/collector/internal/serverless/crypto_market"
)

var Version string

func main() {
	if os.Getenv("MOOX_SPACE_ID") != "crypto_market" {
		panic("MOOX_SPACE_ID must be crypto_market")
	}
	cfg := runtimeapp.DefaultConfig()
	if Version != "" {
		cfg.System.Version = Version
	}
	if _, err := runtimeapp.LoadConfigs(cfg); err != nil {
		panic(fmt.Sprintf("load crypto market SCF config: %v", err))
	}
	cryptomarket.RegisterCloudFunction()
}
