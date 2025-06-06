package cmd

import (
	"fmt"

	"github.com/mooyang-code/moox/cli/internal/database/dboperator"

	"github.com/spf13/cobra"
)

var (
	metaSchemaFile  string
	createDataTable string
	insertDataFile  string
	showSchemaTable string
	showDataTable   string
)

var dbCmd = &cobra.Command{
	Use:     "db",
	Aliases: []string{"database"},
	Short:   "数据库操作命令",
	Long:    "提供对数据库的操作，包括创建元数据表、插入数据、查看表结构、查看表数据等功能。",
	Run: func(cmd *cobra.Command, args []string) {
		// 创建数据库操作类实例
		dbOp, err := dboperator.NewDBOperator(AppConfig)
		if err != nil {
			fmt.Printf("初始化数据库操作失败: %v\n", err)
			return
		}
		defer dbOp.Close()

		if metaSchemaFile != "" {
			err := dbOp.CreateMetaTable(metaSchemaFile)
			if err != nil {
				fmt.Printf("创建元数据表失败: %v\n", err)
				return
			}

			if insertDataFile != "" {
				err := dbOp.InsertDataFromFile(insertDataFile)
				if err != nil {
					fmt.Printf("插入数据失败: %v\n", err)
				}
			}
		} else if createDataTable != "" {
			err := dbOp.CreateTable(createDataTable)
			if err != nil {
				fmt.Printf("创建数据表失败: %v\n", err)
			}
		} else if insertDataFile != "" {
			err := dbOp.InsertDataFromFile(insertDataFile)
			if err != nil {
				fmt.Printf("插入数据失败: %v\n", err)
			}
		} else if showSchemaTable != "" {
			err := dbOp.ShowSchema(showSchemaTable)
			if err != nil {
				fmt.Printf("查看表结构失败: %v\n", err)
			}
		} else if showDataTable != "" {
			err := dbOp.ShowData(showDataTable)
			if err != nil {
				fmt.Printf("查看表数据失败: %v\n", err)
			}
		} else {
			fmt.Println("请指定操作，例如 --meta-schema、--create-table、--insert-data、--show-schema 或 --show-data")
		}
	},
}

func init() {
	rootCmd.AddCommand(dbCmd)

	dbCmd.Flags().StringVar(&metaSchemaFile, "meta-schema", "", "使用SQL文件创建元数据表")
	dbCmd.Flags().StringVar(&createDataTable, "create-table", "", "根据配置信息新建数据表")
	dbCmd.Flags().StringVar(&insertDataFile, "insert-data", "", "向表中插入数据（仅支持 YAML 文件）")
	dbCmd.Flags().StringVar(&showSchemaTable, "show-schema", "", "查看表结构")
	dbCmd.Flags().StringVar(&showDataTable, "show-data", "", "查看表的最近数据")
}

// eg:
// ./moox db --create-meta=t_field
// ./moox db --meta-schema=schema.sql
// ./moox db --insert-data=metadata.yaml
// ./moox db --show-schema=t_dataset
// ./moox db --show-data=t_field
