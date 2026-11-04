package main

import (
	"os"
	"strings"
	// SCF 运行环境不保证有 zoneinfo：内嵌时区数据，交易时段（Asia/Shanghai）按 barclock 换算才不会失败。
	_ "time/tzdata"

	marketdata "github.com/mooyang-code/moox/modules/collector/internal/serverless/market_data"
)

func main() {
	if strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")) == "" {
		panic("MOOX_SPACE_ID is required")
	}
	marketdata.RegisterCloudFunction()
}
