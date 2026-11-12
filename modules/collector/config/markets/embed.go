// Package marketfiles 把各市场的数据文件编进二进制：<市场>/market.yaml、<市场>/route.yaml、<市场>/sources/*.yaml。
// 线上发布只替换二进制、不带配置文件，所以 Collector、云函数、collector-stream 都从这里读。
package marketfiles

import "embed"

// FS 是 config/markets 目录下全部市场的数据文件，路径形如 stockcn/route.yaml。
//
//go:embed */*.yaml */sources/*.yaml
var FS embed.FS
