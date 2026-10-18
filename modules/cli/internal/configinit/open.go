package configinit

import (
	"fmt"
	"os/exec"
	"runtime"
)

// openBrowser 用系统默认浏览器打开 URL。由 CLI 自己调用，而不是让 Agent 的浏览器工具去打开：
// 页面里会出现用户输入的秘密，不应被 Agent 能截图或读取的浏览器看到。
func openBrowser(url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("无法打开浏览器: %w", err)
	}
	go func() { _ = command.Wait() }()
	return nil
}
