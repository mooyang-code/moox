package cmd

import (
	"fmt"
	"path/filepath"
	"strconv"

	"moox/storage"

	pb "github.com/mooyang-code/xData-mini/storage/proto"

	"github.com/spf13/cobra"
)

var (
	interfaceType string
	dataPath      string
	projectID     string
	datasetID     string
	objectID      string
	freq          string
	startTime     string
	endTime       string
	rowID         string
	maxLimit      uint32
)

var storageCmd = &cobra.Command{
	Use:     "storage",
	Aliases: []string{"存储"},
	Short:   "存储服务操作命令",
	Long:    "提供对存储服务的读写操作，包括上传数据到存储服务和从存储服务拉取数据。",
	Run: func(cmd *cobra.Command, args []string) {
		// 检查必要参数
		if interfaceType == "" {
			fmt.Println("请指定存储接口类型 --interface")
			return
		}

		// 根据接口类型执行不同操作
		switch interfaceType {
		case "set", "写入对象数据":
			// 执行上传操作
			fmt.Printf("执行数据上传操作，数据路径: %s, 项目ID: %s, 数据集ID: %s\n",
				dataPath, projectID, datasetID)

			// 创建存储操作实例
			storageOp := storage.NewStorageOperator(AppConfig)

			// 设置默认值
			pID := 1
			if projectID != "" {
				pID, _ = strconv.Atoi(projectID)
			}
			dID := 1
			if datasetID != "" {
				dID, _ = strconv.Atoi(datasetID)
			}

			// 如果未指定对象ID，使用数据路径作为对象ID
			objID := objectID
			if objID == "" {
				objID = filepath.Base(dataPath)
			}

			// 执行写入操作
			dryRun := false // 默认实际写入
			response, err := storageOp.SetData(pID, dID, objID, freq, dryRun)
			if err != nil {
				fmt.Printf("写入操作失败: %v\n", err)
				return
			}

			// 输出结果
			if response.Success {
				fmt.Printf("写入成功，共写入 %d 行数据\n", response.RowsWritten)
				if len(response.FailedRows) > 0 {
					fmt.Printf("其中 %d 行写入失败: %v\n", len(response.FailedRows), response.FailedRows)
				}
			} else {
				fmt.Printf("写入失败: %s\n", response.ErrorMsg)
			}

		case "get", "读取对象数据":
			if objectID == "" {
				fmt.Println("读取操作需要指定数据对象ID --object-id")
				return
			}

			// 执行读取操作
			fmt.Printf("执行数据读取操作，对象ID: %s, 项目ID: %s, 数据集ID: %s\n",
				objectID, projectID, datasetID)

			// 创建存储操作实例
			storageOp := storage.NewStorageOperator(AppConfig)

			// 设置默认值
			pID := 1
			if projectID != "" {
				pID, _ = strconv.Atoi(projectID)
			}

			dID := 1
			if datasetID != "" {
				dID, _ = strconv.Atoi(datasetID)
			}

			// 执行读取操作
			response, err := storageOp.GetData(pID, dID, objectID, freq, startTime, endTime, rowID, maxLimit)
			if err != nil {
				fmt.Printf("读取操作失败: %v\n", err)
				return
			}

			// 输出结果
			if response.Success {
				fmt.Printf("读取成功，共读取 %d 行数据\n", response.RowsRead)

				// 显示数据行内容
				if response.RowsRead > 0 {
					fmt.Println("\n数据内容:")
					for i, row := range response.DataRows {
						fmt.Printf("行 %d: 时间=%s, 行ID=%s\n", i+1, row.Times, row.RowId)

						// 打印字段内容
						for fieldName, field := range row.Fields {
							var valueStr string
							switch field.FieldType {
							case storage.StrField:
								valueStr = field.StrValue
							case storage.IntField:
								valueStr = fmt.Sprintf("%d", field.IntValue)
							case storage.FloatField:
								valueStr = fmt.Sprintf("%f", field.FloatValue)
							default:
								valueStr = "<复杂类型>"
							}
							fmt.Printf("  字段: %s = %s\n", fieldName, valueStr)
						}
						fmt.Println() // 行间空行
					}
				}

				// 显示失败行信息
				if len(response.FailedRows) > 0 {
					fmt.Printf("其中 %d 行读取失败: %v\n", len(response.FailedRows), response.FailedRows)
				}
			} else {
				fmt.Printf("读取失败: %s\n", response.ErrorMsg)
			}

		case "search", "搜索数据":
			if objectID == "" {
				fmt.Println("搜索操作需要指定数据对象ID --object-id")
				return
			}

			// 执行搜索操作
			fmt.Printf("执行数据搜索操作，对象ID: %s, 项目ID: %s, 数据集ID: %s\n",
				objectID, projectID, datasetID)

			// 创建存储操作实例
			storageOp := storage.NewStorageOperator(AppConfig)

			// 设置默认值
			pID := 1
			if projectID != "" {
				pID, _ = strconv.Atoi(projectID)
			}

			dID := 1
			if datasetID != "" {
				dID, _ = strconv.Atoi(datasetID)
			}

			// 执行搜索操作
			response, err := storageOp.SearchData(pID, dID, objectID, freq, startTime, endTime)
			if err != nil {
				fmt.Printf("搜索操作失败: %v\n", err)
				return
			}

			// 输出结果
			if response.Success {
				fmt.Printf("搜索成功，共找到 %d 条结果\n", response.TotalResults)

				// 显示数据行内容
				if len(response.DataRows) > 0 {
					fmt.Println("\n搜索结果:")
					for i, row := range response.DataRows {
						fmt.Printf("结果 %d: 时间=%s, 行ID=%s\n", i+1, row.Times, row.RowId)

						// 打印字段内容
						if row.Fields != nil {
							for fieldName, field := range row.Fields {
								var valueStr string

								// 根据字段类型打印值
								if field.SimpleValue != nil {
									switch v := field.SimpleValue.Value.(type) {
									case *pb.SimpleValue_Str:
										valueStr = v.Str
									case *pb.SimpleValue_Int:
										valueStr = fmt.Sprintf("%d", v.Int)
									case *pb.SimpleValue_Float:
										valueStr = fmt.Sprintf("%f", v.Float)
									case *pb.SimpleValue_Time:
										valueStr = v.Time
									default:
										valueStr = "<复杂类型>"
									}
								} else {
									valueStr = "<空值>"
								}

								fmt.Printf("  字段: %s = %s\n", fieldName, valueStr)
							}
						}
						fmt.Println() // 行间空行
					}
				}

				// 显示失败字段信息
				if len(response.FailedFields) > 0 {
					fmt.Println("搜索过程中的字段错误:")
					for fieldName, errMsg := range response.FailedFields {
						fmt.Printf("  字段 %s: %s\n", fieldName, errMsg)
					}
				}
			} else {
				fmt.Printf("搜索失败: %s\n", response.ErrorMsg)
			}

		default:
			fmt.Printf("不支持的接口类型: %s\n", interfaceType)
		}
	},
}

func init() {
	rootCmd.AddCommand(storageCmd)
	storageCmd.Flags().StringVar(&interfaceType, "interface", "", "请求的存储接口类型 (set/get/search)")
	storageCmd.Flags().StringVar(&dataPath, "data-path", "", "数据路径 (对于搜索操作，可设置为'dry-run'只显示请求不执行)")
	storageCmd.Flags().StringVar(&projectID, "project-id", "", "项目ID")
	storageCmd.Flags().StringVar(&datasetID, "dataset-id", "", "数据集ID")
	storageCmd.Flags().StringVar(&objectID, "object-id", "", "数据对象ID")
	storageCmd.Flags().StringVar(&freq, "freq", "", "时序数据周期")
	storageCmd.Flags().StringVar(&startTime, "start-time", "", "开始时间 (格式: YYYY-MM-DD HH:MM:SS)")
	storageCmd.Flags().StringVar(&endTime, "end-time", "", "结束时间 (格式: YYYY-MM-DD HH:MM:SS)")
	storageCmd.Flags().StringVar(&rowID, "row-id", "", "指定行ID")
	storageCmd.Flags().Uint32Var(&maxLimit, "max-limit", 1000, "最大返回行数")
}

// eg:
// ./moox storage --interface=set --project-id=1 --dataset-id=101 --object-id=BTCUSDT --freq=1H
// ./moox storage --interface=get --project-id=1 --dataset-id=101 --object-id=BTCUSDT --freq=1H
// ./moox storage --interface=search --project-id=1 --dataset-id=101 --object-id=BTCUSDT --freq=1H --start-time="2024-05-01 00:00:00" --end-time="2024-05-02 00:00:00"
// ./moox storage --interface=search --project-id=1 --dataset-id=101 --object-id=BTCUSDT --freq=1H --data-path=dry-run
