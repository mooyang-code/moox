package console

import (
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
)

func TestMethodSpacePolicyThreeTierMatrix(t *testing.T) {
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)

	cases := []struct {
		service string
		method  string
		want    string
	}{
		// 全局服务
		{"auth", "Login", servicecatalog.SpacePolicyGlobal},
		{"ssh", "SftpList", servicecatalog.SpacePolicyGlobal},
		{"secret", "ListSecrets", servicecatalog.SpacePolicyGlobal},
		{"sysdeploy", "ListHosts", servicecatalog.SpacePolicyGlobal},
		{"monitor", "GetHealthOverview", servicecatalog.SpacePolicyGlobal},

		// Storage 全局方法 vs 空间内方法
		{"storage", "ListSpaces", servicecatalog.SpacePolicyGlobal},
		{"storage", "CreateSpace", servicecatalog.SpacePolicyGlobal},
		{"storage", "UpdateSpace", servicecatalog.SpacePolicyGlobal},
		{"storage", "DeleteSpace", servicecatalog.SpacePolicyGlobal},
		{"storage", "ListDatasets", servicecatalog.SpacePolicyRequired},
		{"storage", "QueryTimeSeriesRows", servicecatalog.SpacePolicyRequired},

		// Collector 全局方法 vs 空间内方法
		{"collector", "ListCloudAccounts", servicecatalog.SpacePolicyGlobal},
		{"collector", "CreateCloudAccount", servicecatalog.SpacePolicyGlobal},
		{"collector", "UpdateCloudAccount", servicecatalog.SpacePolicyGlobal},
		{"collector", "DeleteCloudAccount", servicecatalog.SpacePolicyGlobal},
		{"collector", "ListCloudRegions", servicecatalog.SpacePolicyGlobal},
		{"collector", "GetDataTypeConfigs", servicecatalog.SpacePolicyGlobal},
		{"collector", "GetDataTypeConfigWithFields", servicecatalog.SpacePolicyGlobal},
		{"collector", "ListSubjectListers", servicecatalog.SpacePolicyGlobal},
		{"collector", "RegisterDelistNotice", servicecatalog.SpacePolicyGlobal},
		{"collector", "GetLifecycle", servicecatalog.SpacePolicyGlobal},
		{"collector", "ControlLifecycle", servicecatalog.SpacePolicyGlobal},
		{"collector", "GetTaskList", servicecatalog.SpacePolicyRequired},
		{"collector", "ListFunctionPackages", servicecatalog.SpacePolicyRequired},
		{"collector", "ListCloudFunctions", servicecatalog.SpacePolicyRequired},

		// Trade 全局方法 vs 空间内方法
		{"trade", "InspectExchangeCredential", servicecatalog.SpacePolicyGlobal},
		{"trade", "ListBlockedInstruments", servicecatalog.SpacePolicyGlobal},
		{"trade", "UnblockInstrument", servicecatalog.SpacePolicyGlobal},
		{"trade", "ListInstrumentHolders", servicecatalog.SpacePolicyGlobal},
		{"trade", "ListDelists", servicecatalog.SpacePolicyGlobal},
		{"trade", "GetLifecycle", servicecatalog.SpacePolicyGlobal},
		{"trade", "ControlLifecycle", servicecatalog.SpacePolicyGlobal},
		{"trade", "ListLogicalAccounts", servicecatalog.SpacePolicyRequired},
		{"trade", "ListTradingAccounts", servicecatalog.SpacePolicyRequired},
		{"trade", "ListOrders", servicecatalog.SpacePolicyRequired},

		// Strategy 全局 / space_optional / space_required
		{"strategy", "CreateStrategy", servicecatalog.SpacePolicyGlobal},
		{"strategy", "UpdateStrategy", servicecatalog.SpacePolicyGlobal},
		{"strategy", "ListStrategies", servicecatalog.SpacePolicyGlobal},
		{"strategy", "DeleteStrategy", servicecatalog.SpacePolicyGlobal},
		{"strategy", "GetLifecycle", servicecatalog.SpacePolicyGlobal},
		{"strategy", "ControlLifecycle", servicecatalog.SpacePolicyGlobal},
		{"strategy", "GetStrategy", servicecatalog.SpacePolicyOptional},
		{"strategy", "ValidateStrategy", servicecatalog.SpacePolicyOptional},
		{"strategy", "ListStrategyInstances", servicecatalog.SpacePolicyRequired},
		{"strategy", "CreateStrategyInstance", servicecatalog.SpacePolicyRequired},
		{"strategy", "ListStrategyResults", servicecatalog.SpacePolicyRequired},
		{"strategy", "ListReplays", servicecatalog.SpacePolicyRequired},
		{"strategy", "ListReplayBars", servicecatalog.SpacePolicyRequired},

		// ActivityLog space_optional
		{"activity", "ListActivityLogs", servicecatalog.SpacePolicyOptional},

		// FactorMgr 全局 / space_optional / space_required
		{"factor-mgr", "CreateFactor", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "UpdateFactor", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "DeleteFactor", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "ImportFactorCatalog", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "ValidateFactorSpec", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "ListFactorFunctions", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "ListFactorCategories", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "UpsertFactorCategory", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "DeleteFactorCategory", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "SetFactorCategory", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "GetStatus", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "PublishUdf", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "ListUdfs", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "GetUdf", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "ActivateUdf", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "GetLifecycle", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "ControlLifecycle", servicecatalog.SpacePolicyGlobal},
		{"factor-mgr", "ListFactorTasks", servicecatalog.SpacePolicyOptional},
		{"factor-mgr", "ListFactors", servicecatalog.SpacePolicyOptional},
		{"factor-mgr", "GetFactor", servicecatalog.SpacePolicyOptional},
		{"factor-mgr", "CreateFactorTask", servicecatalog.SpacePolicyRequired},
		{"factor-mgr", "DeleteFactorTask", servicecatalog.SpacePolicyRequired},
		{"factor-mgr", "AddTaskMember", servicecatalog.SpacePolicyRequired},
		{"factor-mgr", "RecalcFactors", servicecatalog.SpacePolicyRequired},
		{"factor-mgr", "PreviewFactor", servicecatalog.SpacePolicyRequired},

		// Archive space_required
		{"archive", "GetArchiveOverview", servicecatalog.SpacePolicyRequired},
		{"archive", "ListArchiveTasks", servicecatalog.SpacePolicyRequired},
		{"archive", "ClearArchiveUnrecoverable", servicecatalog.SpacePolicyRequired},
	}

	for _, tc := range cases {
		t.Run(tc.service+"/"+tc.method, func(t *testing.T) {
			require.Equal(t, tc.want, methodSpacePolicy(catalog, tc.service, tc.method))
		})
	}
}
