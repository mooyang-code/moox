// Package webui 内嵌管理台前端（web/dist 的构建产物），由 Admin 的浏览器入口提供。
//
// 仓库里 dist/ 只提交占位的 .gitkeep；scripts/build/build.sh 编译 admin 前把 web/dist 拷进来，编译完删掉，
// 不改动任何已提交的文件。没有拷入前端时，入口对页面请求返回“前端没有打包进这个程序”。
package webui

import (
	"embed"
	"io/fs"
)

// all: 前缀才会带上以 _ 开头的文件（构建产物里有 _plugin-vue_export-helper 这类分块）。
//
//go:embed all:dist
var dist embed.FS

// FS 返回前端文件，根目录就是 web/dist。
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
