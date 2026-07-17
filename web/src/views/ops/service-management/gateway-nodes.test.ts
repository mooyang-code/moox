import fs from "node:fs";
import path from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { callControl } from "@/api/admin/http";
import {
  deleteGatewayNode,
  deleteServiceDeployment,
  getGatewayNodeRoutes,
  listGatewayNodes,
  listServiceDeployments,
  updateServiceDeployment
} from "@/api/admin/sysdeploy";
import type { ServiceDeploymentInput } from "@/api/admin/types";
import {
  gatewayHashState,
  gatewayNodeOnlineState,
  createLatestRequestGuard,
  runModalSubmission,
  serviceDeploymentRowKey,
  validateGatewayControlURL,
  validateGatewayDeployment
} from "@/views/settings/service-deployments/health";

vi.mock("@/api/admin/http", () => ({ callControl: vi.fn() }));

const mockedCallControl = vi.mocked(callControl);
const normalizeSource = (source: string) => source.replace(/\s+/g, "").replace(/'/g, '"');

describe("gateway node and service instance contracts", () => {
  beforeEach(() => mockedCallControl.mockReset());

  it("keeps the four service management tabs in the required order", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    const normalized = normalizeSource(source);
    const positions = ["网关节点", "服务实例", "可用性监控", "应用指标"].map(label => normalized.indexOf(`label:"${label}"`));
    expect(positions.every(position => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((left, right) => left - right));
  });

  it("labels heartbeat and route hash state without relying on color alone", () => {
    expect(gatewayNodeOnlineState("2026-07-15T10:00:00Z", new Date("2026-07-15T10:01:00Z"))).toEqual({
      state: "online",
      label: "在线"
    });
    expect(gatewayNodeOnlineState("", new Date("2026-07-15T10:01:00Z")).label).toBe("未上报");
    expect(gatewayHashState("expected", "applied")).toEqual({ state: "mismatch", label: "待同步" });
    expect(gatewayHashState("same", "same")).toEqual({ state: "synced", label: "已同步" });
  });

  it("propagates node filters and composite identities to deployment operations", async () => {
    mockedCallControl.mockResolvedValue({ deployments: [] });
    await listServiceDeployments({ node_id: "gateway-gz-122", gateway_enabled: true });
    expect(mockedCallControl).toHaveBeenLastCalledWith("sysdeploy", "ListServiceDeployments", {
      node_id: "gateway-gz-122",
      gateway_enabled: true
    });

    const deployment = { node_id: "gateway-gz-122", service_name: "monitor" } as ServiceDeploymentInput;
    await updateServiceDeployment("gateway-gz-122", "monitor", deployment);
    expect(mockedCallControl).toHaveBeenLastCalledWith("sysdeploy", "UpdateServiceDeployment", {
      node_id: "gateway-gz-122",
      service_name: "monitor",
      deployment
    });
    await deleteServiceDeployment("gateway-gz-122", "monitor");
    expect(mockedCallControl).toHaveBeenLastCalledWith("sysdeploy", "DeleteServiceDeployment", {
      node_id: "gateway-gz-122",
      service_name: "monitor"
    });
    expect(serviceDeploymentRowKey(deployment)).toBe("gateway-gz-122:monitor");
  });

  it("supports gateway node listing, route inspection, and deletion APIs", async () => {
    mockedCallControl.mockResolvedValue({ nodes: [] });
    await listGatewayNodes({ node_id: "gateway-gz-122", status: "enabled" });
    expect(mockedCallControl).toHaveBeenLastCalledWith("sysdeploy", "ListGatewayNodes", {
      node_id: "gateway-gz-122",
      status: "enabled"
    });
    await getGatewayNodeRoutes("gateway-gz-122");
    expect(mockedCallControl).toHaveBeenLastCalledWith("sysdeploy", "GetGatewayNodeRoutes", { node_id: "gateway-gz-122" });
    await deleteGatewayNode("gateway-gz-122");
    expect(mockedCallControl).toHaveBeenLastCalledWith("sysdeploy", "DeleteGatewayNode", { node_id: "gateway-gz-122" });
  });

  it("requires loopback host and a tRPC path only when gateway exposure is enabled", () => {
    const valid = {
      gateway_enabled: true,
      host: "127.0.0.1",
      gateway_path: "trpc.moox.monitor.Monitor",
      gateway_service_id: "monitor"
    } as ServiceDeploymentInput;
    expect(validateGatewayDeployment(valid)).toBe("");
    expect(validateGatewayDeployment({ ...valid, host: "10.0.0.8" })).toContain("127.0.0.1");
    expect(validateGatewayDeployment({ ...valid, host: "::1" })).toBe("");
    expect(validateGatewayDeployment({ ...valid, gateway_path: "" })).toContain("tRPC");
    expect(validateGatewayDeployment({ ...valid, gateway_service_id: "" })).toContain("service ID");
    expect(validateGatewayDeployment({ ...valid, gateway_enabled: false, host: "10.0.0.8", gateway_path: "" })).toBe("");
  });

  it("accepts HTTPS and loopback HTTP gateway URLs but rejects unsafe origins", () => {
    expect(validateGatewayControlURL("https://gateway.example.com:9527")).toBe("");
    expect(validateGatewayControlURL("http://127.0.0.1:11002")).toBe("");
    expect(validateGatewayControlURL("http://[::1]:11002")).toBe("");
    expect(validateGatewayControlURL("http://localhost:11002")).toBe("");
    expect(validateGatewayControlURL("http://10.0.0.8:11002")).not.toBe("");
    expect(validateGatewayControlURL("https://user:secret@gateway.example.com")).not.toBe("");
    expect(validateGatewayControlURL("https://gateway.example.com/path?token=secret")).not.toBe("");
    expect(validateGatewayControlURL("not a url")).not.toBe("");
  });

  it("keeps modal editors open for validation and API failures", async () => {
    const submit = vi.fn().mockResolvedValue(undefined);
    expect(await runModalSubmission(() => "请补全字段", submit)).toBe(false);
    expect(submit).not.toHaveBeenCalled();

    const onError = vi.fn();
    expect(await runModalSubmission(() => "", vi.fn().mockRejectedValue(new Error("unavailable")), onError)).toBe(false);
    expect(onError).toHaveBeenCalledOnce();
    expect(await runModalSubmission(() => "", submit)).toBe(true);
  });

  it("rejects stale list responses after a newer filter generation starts", () => {
    const guard = createLatestRequestGuard();
    const first = guard.begin();
    const second = guard.begin();
    expect(guard.isCurrent(first)).toBe(false);
    expect(guard.isCurrent(second)).toBe(true);
    guard.invalidate();
    expect(guard.isCurrent(second)).toBe(false);
  });

  it("uses activated refresh with bounded ticker cleanup and no per-instance health mapping", () => {
    const nodesSource = fs.readFileSync(path.resolve(__dirname, "gateway-nodes.vue"), "utf8");
    const instancesSource = fs.readFileSync(path.resolve(__dirname, "../../settings/service-deployments/index.vue"), "utf8");
    const normalizedNodes = normalizeSource(nodesSource);
    const normalizedInstances = normalizeSource(instancesSource);
    expect(nodesSource).toContain("onActivated");
    expect(nodesSource).toContain("onDeactivated");
    expect(nodesSource).toContain("stopRefreshTimer");
    expect(instancesSource).not.toContain("healthLabel");
    expect(instancesSource).not.toContain("monitorApi");
    expect(nodesSource).toContain("reportControlError");
    expect(instancesSource).toContain("reportControlError");
    expect(normalizedNodes).toContain("},reportControlError);");
    expect(normalizedInstances).toContain("},reportControlError);");
  });
});
