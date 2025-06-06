package cmd

import (
	"fmt"

	"github.com/mooyang-code/moox/cli/internal/message"

	"github.com/spf13/cobra"
)

// 消费消息的子命令
var consumeCmd = &cobra.Command{
	Use:   "consume",
	Short: "消费消息队列",
	Long:  "从消息队列中消费消息，实时处理新到达的消息",
	Run: func(cmd *cobra.Command, args []string) {
		// 创建消息操作实例
		msgOp := message.NewMessageOperator(AppConfig)
		defer msgOp.Close()

		// 实现消费消息的逻辑
		err := msgOp.ConsumeMessages()
		if err != nil {
			fmt.Printf("消费消息失败: %v\n", err)
		}
	},
}

var messageCmd = &cobra.Command{
	Use:     "msg",
	Aliases: []string{"message"},
	Short:   "消息队列操作命令",
	Long:    "提供对消息队列的操作，用于消费消息队列中的消息",
	Run: func(cmd *cobra.Command, args []string) {
		// 使用子命令时不执行任何操作，只显示帮助信息
		cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(messageCmd)

	// 添加消费消息的子命令
	messageCmd.AddCommand(consumeCmd)
}

// eg:
// ./moox message consume  # 消费消息队列，实时处理新消息
