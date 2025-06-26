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
		// 检查必要的配置
		if AppConfig.Moox == nil || AppConfig.Moox.AuthTarget == "" {
			fmt.Println("错误：未配置认证服务地址(moox.auth_target)")
			return
		}

		// 交互式输入用户信息
		reader := bufio.NewReader(os.Stdin)

		// 如果命令行没有指定用户名，交互式输入
		if username == "" {
			fmt.Print("请输入用户名: ")
			input, err := reader.ReadString('\n')
			if err != nil {
				fmt.Printf("读取用户名失败: %v\n", err)
				return
			}
			username = strings.TrimSpace(input)
		}

		if username == "" {
			fmt.Println("用户名不能为空")
			return
		}

		// 如果命令行没有指定密码，交互式输入（支持二次确认）
		if password == "" {
			// 第一次输入密码
			fmt.Print("请输入密码: ")
			passwordBytes, err := term.ReadPassword(int(syscall.Stdin))
			if err != nil {
				fmt.Printf("\n读取密码失败: %v\n", err)
				return
			}
			password = string(passwordBytes)
			fmt.Println() // 换行

			if password == "" {
				fmt.Println("密码不能为空")
				return
			}

			// 第二次输入密码确认
			fmt.Print("请再次输入密码: ")
			confirmPasswordBytes, err := term.ReadPassword(int(syscall.Stdin))
			if err != nil {
				fmt.Printf("\n读取密码失败: %v\n", err)
				return
			}
			confirmPassword := string(confirmPasswordBytes)
			fmt.Println() // 换行

			// 验证两次密码是否一致
			if password != confirmPassword {
				fmt.Println("两次输入的密码不一致，请重新运行命令")
				return
			}

			fmt.Println("密码确认成功")
		}

		// 可选的昵称
		if nickname == "" {
			fmt.Print("请输入昵称 (可选, 直接回车跳过): ")
			input, err := reader.ReadString('\n')
			if err != nil {
				fmt.Printf("读取昵称失败: %v\n", err)
				return
			}
			nickname = strings.TrimSpace(input)
		}

		// 可选的邮箱
		if email == "" {
			fmt.Print("请输入邮箱 (可选, 直接回车跳过): ")
			input, err := reader.ReadString('\n')
			if err != nil {
				fmt.Printf("读取邮箱失败: %v\n", err)
				return
			}
			email = strings.TrimSpace(input)
		}

		// 创建认证服务操作实例
		authOp := auth.NewAuthOperator(AppConfig)

		// 调用注册接口
		fmt.Printf("\n正在注册用户 '%s'...\n", username)
		ctx := context.Background()
		rsp, err := authOp.RegisterUser(ctx, username, password, nickname, email)
		if err != nil {
			fmt.Printf("注册失败: %v\n", err)
			return
		}

		// 处理响应
		if rsp.RetInfo != nil && rsp.RetInfo.Code == 0 { // SUCCESS = 0
			fmt.Printf("注册成功！\n")
			fmt.Printf("用户ID: %s\n", rsp.UserId)
			if rsp.UserInfo != nil {
				fmt.Printf("用户名: %s\n", rsp.UserInfo.Username)
				if rsp.UserInfo.Nickname != "" {
					fmt.Printf("昵称: %s\n", rsp.UserInfo.Nickname)
				}
				if rsp.UserInfo.Email != "" {
					fmt.Printf("邮箱: %s\n", rsp.UserInfo.Email)
				}
				fmt.Printf("状态: %v\n", rsp.UserInfo.Status)
				fmt.Printf("角色: %v\n", rsp.UserInfo.Role)
			}
		} else {
			errorMsg := "未知错误"
			errorCode := int32(-1)
			if rsp.RetInfo != nil {
				errorMsg = rsp.RetInfo.Msg
				errorCode = int32(rsp.RetInfo.Code)
			}
			fmt.Printf("注册失败: %s (错误码: %v)\n", errorMsg, errorCode)
		}
	},
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
