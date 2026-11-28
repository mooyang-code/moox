import { describe, expect, it } from "vitest";
import {
  archiveTasksPath,
  collectorTaskDetailHref,
  collectorTasksPath,
  dataSourcesPath,
  factorDefinitionsPath,
  factorTaskDetailHref,
  factorTaskRecalcHref,
  factorTasksLocation,
  factorTasksPath,
  hashHref,
  opsDeploymentsPath,
  opsHostsPath,
  opsLogsPath,
  opsMonitorPath,
  settingsSecretsPath,
  settingsSpacesPath,
  strategyInstanceDetailLink,
  strategyTasksPath,
  tradingAccountDetailLink,
  tradingAccountDetailPath,
  tradingAccountsPath,
  tradingBlockedPath
} from "./links";

describe("跨模块跳转链接", () => {
  it("构造采集、因子、数据与归档链接", () => {
    expect(dataSourcesPath()).toBe("/data/sources");
    expect(collectorTasksPath({ tab: "results", dataset: "ds_1", freq: "1m" })).toBe(
      "/collector/tasks?tab=results&dataset=ds_1&freq=1m"
    );
    expect(collectorTaskDetailHref("ct_1")).toBe("#/collector/tasks?detail=ct_1");
    expect(factorTasksPath({ tab: "results", task: "ft_1" })).toBe("/factor/tasks?tab=results&task=ft_1");
    expect(factorTasksLocation({ tab: "results", task: "ft_1" })).toEqual({
      path: "/factor/tasks",
      query: { tab: "results", task: "ft_1" }
    });
    expect(factorTaskDetailHref("ft_1")).toBe("#/factor/tasks?detail=ft_1");
    expect(factorTaskRecalcHref("ft_1")).toBe("#/factor/tasks?tab=recalc&task=ft_1");
    expect(factorDefinitionsPath()).toBe("/factor/definitions");
    expect(archiveTasksPath("ds_1")).toBe("/archive/tasks?task=ds_1");
  });

  it("构造策略、交易、运维与设置链接", () => {
    expect(strategyTasksPath()).toBe("/strategy/tasks");
    expect(strategyInstanceDetailLink("i1")).toEqual({ name: "strategy-detail", params: { instanceId: "i1" } });
    expect(tradingAccountsPath("paper")).toBe("/trading/paper");
    expect(tradingAccountsPath("paper", { list: "execution" })).toBe("/trading/paper?list=execution");
    expect(tradingAccountDetailLink("la-1")).toEqual({ name: "trading-account-detail", params: { logical_account_id: "la-1" } });
    expect(tradingAccountDetailPath("la/1")).toBe("/trading/accounts/la%2F1");
    expect(tradingBlockedPath()).toBe("/trading/blocked");
    expect(opsMonitorPath()).toBe("/ops/monitor");
    expect(hashHref(opsHostsPath({ tab: "monitor", hostId: "h1" }))).toBe("#/ops/hosts?tab=monitor&host_id=h1");
    expect(opsDeploymentsPath({ tab: "services", hostId: "h1", componentId: "c1" })).toBe(
      "/ops/deployments?tab=services&host_id=h1&component_id=c1"
    );
    expect(opsLogsPath("activity")).toBe("/ops/logs?tab=activity");
    expect(settingsSpacesPath()).toBe("/settings/spaces");
    expect(settingsSecretsPath()).toBe("/settings/secrets");
  });
});
