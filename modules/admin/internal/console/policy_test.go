package console

import (
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
)

func TestMethodSpacePolicyThreeTierMatrix(t *testing.T) {
	cases := []struct {
		service string
		method  string
		want    spacePolicy
	}{
		// 全局服务
		{"auth", "Login", spacePolicyGlobal},
		{"ssh", "SftpList", spacePolicyGlobal},
		{"secret", "ListSecrets", spacePolicyGlobal},
		{"sysdeploy", "ListHosts", spacePolicyGlobal},
		{"monitor", "GetHealthOverview", spacePolicyGlobal},
		{"eventbus", "GetOverview", spacePolicyGlobal},

		// Storage 全局方法 vs 空间内方法
		{"storage", "ListSpaces", spacePolicyGlobal},
		{"storage", "CreateSpace", spacePolicyGlobal},
		{"storage", "UpdateSpace", spacePolicyGlobal},
		{"storage", "DeleteSpace", spacePolicyGlobal},
		{"storage", "ListDatasets", spacePolicyRequired},
		{"storage", "QueryTimeSeriesRows", spacePolicyRequired},

		// Collector 全局方法 vs 空间内方法
		{"collector", "ListCloudAccounts", spacePolicyGlobal},
		{"collector", "CreateCloudAccount", spacePolicyGlobal},
		{"collector", "UpdateCloudAccount", spacePolicyGlobal},
		{"collector", "DeleteCloudAccount", spacePolicyGlobal},
		{"collector", "ListCloudRegions", spacePolicyGlobal},
		{"collector", "GetDataTypeConfigs", spacePolicyGlobal},
		{"collector", "GetDataTypeConfigWithFields", spacePolicyGlobal},
		{"collector", "ListSubjectListers", spacePolicyGlobal},
		{"collector", "RegisterDelistNotice", spacePolicyGlobal},
		{"collector", "GetLifecycle", spacePolicyGlobal},
		{"collector", "ControlLifecycle", spacePolicyGlobal},
		{"collector", "GetTaskList", spacePolicyRequired},
		{"collector", "ListFunctionPackages", spacePolicyRequired},
		{"collector", "ListCloudFunctions", spacePolicyRequired},

		// Trade 全局方法 vs 空间内方法
		{"trade", "InspectExchangeCredential", spacePolicyGlobal},
		{"trade", "ListBlockedInstruments", spacePolicyGlobal},
		{"trade", "UnblockInstrument", spacePolicyGlobal},
		{"trade", "ListInstrumentHolders", spacePolicyGlobal},
		{"trade", "ListDelists", spacePolicyGlobal},
		{"trade", "GetLifecycle", spacePolicyGlobal},
		{"trade", "ControlLifecycle", spacePolicyGlobal},
		{"trade", "ListLogicalAccounts", spacePolicyRequired},
		{"trade", "ListTradingAccounts", spacePolicyRequired},
		{"trade", "ListOrders", spacePolicyRequired},

		// Strategy 全局 / space_optional / space_required
		{"strategy", "CreateStrategy", spacePolicyGlobal},
		{"strategy", "UpdateStrategy", spacePolicyGlobal},
		{"strategy", "ListStrategies", spacePolicyGlobal},
		{"strategy", "DeleteStrategy", spacePolicyGlobal},
		{"strategy", "GetLifecycle", spacePolicyGlobal},
		{"strategy", "ControlLifecycle", spacePolicyGlobal},
		{"strategy", "GetStrategy", spacePolicyOptional},
		{"strategy", "ValidateStrategy", spacePolicyOptional},
		{"strategy", "ListStrategyInstances", spacePolicyRequired},
		{"strategy", "CreateStrategyInstance", spacePolicyRequired},
		{"strategy", "ListStrategyResults", spacePolicyRequired},
		{"strategy", "ListReplays", spacePolicyRequired},
		{"strategy", "ListReplayBars", spacePolicyRequired},

		// ActivityLog space_optional
		{"activity", "ListActivityLogs", spacePolicyOptional},

		// FactorMgr 全局 / space_optional / space_required
		{"factor-mgr", "CreateFactor", spacePolicyGlobal},
		{"factor-mgr", "UpdateFactor", spacePolicyGlobal},
		{"factor-mgr", "DeleteFactor", spacePolicyGlobal},
		{"factor-mgr", "ValidateFactorSpec", spacePolicyGlobal},
		{"factor-mgr", "ListFactorFunctions", spacePolicyGlobal},
		{"factor-mgr", "ListFactorCategories", spacePolicyGlobal},
		{"factor-mgr", "UpsertFactorCategory", spacePolicyGlobal},
		{"factor-mgr", "DeleteFactorCategory", spacePolicyGlobal},
		{"factor-mgr", "SetFactorCategory", spacePolicyGlobal},
		{"factor-mgr", "GetStatus", spacePolicyGlobal},
		{"factor-mgr", "PublishUdf", spacePolicyGlobal},
		{"factor-mgr", "ListUdfs", spacePolicyGlobal},
		{"factor-mgr", "GetUdf", spacePolicyGlobal},
		{"factor-mgr", "ActivateUdf", spacePolicyGlobal},
		{"factor-mgr", "ListFactorTasks", spacePolicyOptional},
		{"factor-mgr", "ListFactors", spacePolicyOptional},
		{"factor-mgr", "GetFactor", spacePolicyOptional},
		{"factor-mgr", "CreateFactorTask", spacePolicyRequired},
		{"factor-mgr", "DeleteFactorTask", spacePolicyRequired},
		{"factor-mgr", "AddTaskMember", spacePolicyRequired},
		{"factor-mgr", "RecalcFactors", spacePolicyRequired},
		{"factor-mgr", "PreviewFactor", spacePolicyRequired},

		// Archive 全局 / space_required
		{"archive", "GetLifecycle", spacePolicyGlobal},
		{"archive", "ControlLifecycle", spacePolicyGlobal},
		{"archive", "GetArchiveOverview", spacePolicyRequired},
		{"archive", "ListArchiveTasks", spacePolicyRequired},
	}

	for _, tc := range cases {
		t.Run(tc.service+"/"+tc.method, func(t *testing.T) {
			require.Equal(t, tc.want, methodSpacePolicy(tc.service, tc.method))
		})
	}
}

// 策略表里的每个方法都必须存在于内嵌服务目录中，且不能同时被标成 global 和 space_optional。
func TestSpacePolicyTablesMatchCatalog(t *testing.T) {
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)

	catalogMethods := map[string]map[string]bool{}
	for _, component := range catalog.Components {
		for _, service := range component.Services {
			if service.ConsoleName == "" {
				continue
			}
			if catalogMethods[service.ConsoleName] == nil {
				catalogMethods[service.ConsoleName] = map[string]bool{}
			}
			for _, method := range service.Methods {
				catalogMethods[service.ConsoleName][method.Name] = true
			}
		}
	}

	for serviceID := range globalServices {
		require.Contains(t, catalogMethods, serviceID, "globalServices[%s] 不在 catalog.yaml 中", serviceID)
	}
	for serviceID, methods := range globalMethods {
		require.NotContains(t, globalServices, serviceID, "%s 已在 globalServices，不应重复列在 globalMethods", serviceID)
		for method := range methods {
			require.True(t, catalogMethods[serviceID][method], "globalMethods[%s][%s] 不在 catalog.yaml 中", serviceID, method)
			require.False(t, optionalSpaceMethods[serviceID][method], "%s/%s 不能同时出现在 globalMethods 与 optionalSpaceMethods", serviceID, method)
		}
	}
	for serviceID, methods := range optionalSpaceMethods {
		require.NotContains(t, globalServices, serviceID, "%s 已在 globalServices，不应重复列在 optionalSpaceMethods", serviceID)
		for method := range methods {
			require.True(t, catalogMethods[serviceID][method], "optionalSpaceMethods[%s][%s] 不在 catalog.yaml 中", serviceID, method)
		}
	}
}
