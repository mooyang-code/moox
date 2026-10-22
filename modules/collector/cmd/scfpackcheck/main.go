// scfpackcheck 是 SCF 打包脚本用的检查工具：
//
//	scfpackcheck normpath <路径>   输出词法规范化后的路径
//	scfpackcheck check <包目录>    检查包里的配置合法且不含凭据，不通过时以非零退出
package main

import (
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/collector/internal/scfpack"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: scfpackcheck normpath <path> | check <package-dir>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "normpath":
		fmt.Println(scfpack.Normpath(os.Args[2]))
	case "check":
		if err := scfpack.CheckPackage(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}
