package main

import (
	"os"
	"strings"

	marketdata "github.com/mooyang-code/moox/modules/collector/internal/serverless/market_data"
)

func main() {
	if strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")) == "" {
		panic("MOOX_SPACE_ID is required")
	}
	marketdata.RegisterCloudFunction()
}
