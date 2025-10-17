package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/mooyang-code/moox/cli/internal/auth"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	username string
	password string
	nickname string
	email    string
)

var authCmd = &cobra.Command{
	Use:     "auth",
	Aliases: []string{"认证"},
	Short:   "用户认证相关操作",
	Long:    "提供用户注册、登录等认证相关的操作功能。",
}

var registerCmd = &cobra.Command{
	Use:     "register",
	Aliases: []string{"注册"},
	Short:   "用户注册",
	Long:    "注册新用户账号，需要输入用户名和密码。",
	Run: func(cmd *cobra.Command, args []string) {
		// 检查配置是否已加载
		if AppConfig == nil {
			fmt.Println("❌ 错误：系统配置未正确加载")
			fmt.Println("💡 请确保配置文件存在并可访问，或使用 MOOX_CONFIG 环境变量指定配置文件路径")
			return
		}

		// 检查认证服务配置
		if AppConfig.MooX == nil {
			fmt.Println("❌ 错误：未找到 MooX 服务配置")
			fmt.Println("💡 请在配置文件中添加 moox.auth_target 配置项")
			showConfigExample()
			return
		}

		if AppConfig.MooX.AuthTarget == "" {
			fmt.Println("❌ 错误：未配置认证服务地址")
			fmt.Println("💡 请在配置文件中设置 moox.auth_target，例如: 127.0.0.1:18200")
			showConfigExample()
			return
		}

		fmt.Println("🚀 欢迎使用 MooX 用户注册功能！")
		fmt.Printf("📡 认证服务地址: %s\n\n", AppConfig.MooX.AuthTarget)

		// 交互式收集用户信息
		if err := collectUserInfo(); err != nil {
			fmt.Printf("❌ 收集用户信息失败: %v\n", err)
			return
		}

		// 执行用户注册
		if err := performUserRegistration(); err != nil {
			fmt.Printf("❌ 用户注册失败: %v\n", err)
			return
		}
	},
}

// showConfigExample 显示配置示例
func showConfigExample() {
	fmt.Println("\n📝 配置文件示例 (config/cli.yaml):")
	fmt.Println("```yaml")
	fmt.Println("moox:")
	fmt.Println("  auth_target: \"127.0.0.1:18200\"")
	fmt.Println("```")
	fmt.Println()
}

// collectUserInfo 收集用户信息
func collectUserInfo() error {
	reader := bufio.NewReader(os.Stdin)

	// 收集用户名
	if username == "" {
		fmt.Print("👤 请输入用户名: ")
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("读取用户名失败: %v", err)
		}
		username = strings.TrimSpace(input)
	}

	if username == "" {
		return fmt.Errorf("用户名不能为空")
	}

	// 收集密码
	if password == "" {
		// 第一次输入密码
		fmt.Print("🔒 请输入密码: ")
		passwordBytes, err := term.ReadPassword(int(syscall.Stdin))
		if err != nil {
			return fmt.Errorf("读取密码失败: %v", err)
		}
		password = string(passwordBytes)
		fmt.Println() // 换行

		if password == "" {
			return fmt.Errorf("密码不能为空")
		}

		// 确认密码
		fmt.Print("🔒 请再次输入密码: ")
		confirmPasswordBytes, err := term.ReadPassword(int(syscall.Stdin))
		if err != nil {
			return fmt.Errorf("读取确认密码失败: %v", err)
		}
		confirmPassword := string(confirmPasswordBytes)
		fmt.Println() // 换行

		// 验证两次密码是否一致
		if password != confirmPassword {
			return fmt.Errorf("两次输入的密码不一致")
		}

		fmt.Println("✅ 密码确认成功")
	}

	// 收集昵称（可选）
	if nickname == "" {
		fmt.Print("😊 请输入昵称 (可选, 直接回车跳过): ")
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("读取昵称失败: %v", err)
		}
		nickname = strings.TrimSpace(input)
	}

	// 收集邮箱（可选）
	if email == "" {
		fmt.Print("📧 请输入邮箱 (可选, 直接回车跳过): ")
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("读取邮箱失败: %v", err)
		}
		email = strings.TrimSpace(input)
	}

	return nil
}

// performUserRegistration 执行用户注册
func performUserRegistration() error {
	// 创建认证服务操作实例
	authOp := auth.NewAuthOperator(AppConfig)

	// 调用注册接口
	fmt.Printf("\n🔄 正在注册用户 '%s'...\n", username)
	ctx := context.Background()
	rsp, err := authOp.RegisterUser(ctx, username, password, nickname, email)
	if err != nil {
		return fmt.Errorf("请求注册接口失败: %v", err)
	}

	// 处理响应
	if rsp.RetInfo != nil && rsp.RetInfo.Code == 0 { // SUCCESS = 0
		fmt.Println("🎉 注册成功！")
		fmt.Printf("👤 用户ID: %s\n", rsp.UserId)
		if rsp.UserInfo != nil {
			fmt.Printf("📛 用户名: %s\n", rsp.UserInfo.Username)
			if rsp.UserInfo.Nickname != "" {
				fmt.Printf("😊 昵称: %s\n", rsp.UserInfo.Nickname)
			}
			if rsp.UserInfo.Email != "" {
				fmt.Printf("📧 邮箱: %s\n", rsp.UserInfo.Email)
			}
			fmt.Printf("✅ 状态: %v\n", rsp.UserInfo.Status)
			fmt.Printf("🔰 角色: %v\n", rsp.UserInfo.Role)
		}
	} else {
		errorMsg := "未知错误"
		errorCode := int32(-1)
		if rsp.RetInfo != nil {
			errorMsg = rsp.RetInfo.Msg
			errorCode = int32(rsp.RetInfo.Code)
		}
		return fmt.Errorf("%s (错误码: %v)", errorMsg, errorCode)
	}

	return nil
}

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(registerCmd)

	// 添加可选的命令行参数
	registerCmd.Flags().StringVar(&username, "username", "", "用户名")
	registerCmd.Flags().StringVar(&password, "password", "", "密码 (不推荐通过命令行参数设置)")
	registerCmd.Flags().StringVar(&nickname, "nickname", "", "昵称 (可选)")
	registerCmd.Flags().StringVar(&email, "email", "", "邮箱 (可选)")
}
