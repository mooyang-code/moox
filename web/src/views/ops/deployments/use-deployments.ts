import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { usePolling } from "@/hooks/usePolling";
import { useRoute, useRouter } from "vue-router";
import { decodeCatalog, getCatalog, getHostRoutes, listAllDeploymentHosts, listAllDeployments } from "@/api/admin/sysdeploy";
import type { ComponentCatalog, ComponentDeployment, DeploymentHost, HostRoutesResponse } from "@/api/admin/types";
import { monitorApi, type HealthOverview } from "@/api/monitor";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { loadHostRoutes, deploymentRows } from "./model";

export function useDeployments() {
  const route = useRoute();
  const router = useRouter();
  const catalog = ref<ComponentCatalog>();
  const catalogHash = ref("");
  const controlHostId = ref("");
  const hosts = ref<DeploymentHost[]>([]);
  const deployments = ref<ComponentDeployment[]>([]);
  const overview = ref<HealthOverview>();
  const routes = ref<Record<string, HostRoutesResponse>>({});
  const routeErrors = ref<Record<string, string>>({});
  const loading = ref(false);
  const error = ref("");
  const monitorError = ref("");
  const loaded = ref(false);
  const search = ref("");
  // 状态筛选写进 URL（?filter=）：默认“启用中”不写；“全部”含已停用，替代原来重复的“显示已停用”复选框
  const filter = computed({
    get: () => {
      const value = query(route.query.filter);
      return ["abnormal", "disabled", "all"].includes(value) ? value : "enabled";
    },
    set: value => {
      const next = { ...route.query, filter: value === "enabled" ? undefined : value };
      void router.replace({ query: Object.fromEntries(Object.entries(next).filter(([, item]) => item !== undefined)) });
    }
  });
  const selectedHostId = ref("");
  const drawerKey = ref("");
  const guard = createLatestRequestGuard();
  let appliedLocator = "";
  const query = (value: unknown) => (typeof value === "string" ? value : "");
  const tab = computed({
    get: () => (route.query.tab === "routes" || route.query.tab === "topology" ? route.query.tab : "services"),
    set: (value: string) => {
      void router.replace({ query: { ...route.query, tab: value } });
    }
  });
  const groups = computed(() =>
    hosts.value.map(host => ({
      host,
      rows: deploymentRows(
        host,
        deployments.value,
        catalog.value?.components || [],
        monitorError.value || error.value ? undefined : overview.value
      )
    }))
  );
  const unregistered = computed(() => {
    const registered = new Set(deployments.value.map(item => `${item.host_id}\0${item.component_id}`));
    return (overview.value?.unregistered || []).filter(
      item => !registered.has(`${item.host_id || ""}\0${item.component_id || ""}`)
    );
  });
  const requestedHost = computed(() => query(route.query.host_id));
  const requestedComponent = computed(() => query(route.query.component_id));
  const locatorError = computed(() => {
    if (!loaded.value) return "";
    if (requestedHost.value && !hosts.value.some(host => host.host_id === requestedHost.value))
      return `未找到主机 ${requestedHost.value}`;
    if (
      requestedComponent.value &&
      !groups.value.some(
        group =>
          (!requestedHost.value || group.host.host_id === requestedHost.value) &&
          group.rows.some(row => row.deployment.component_id === requestedComponent.value)
      )
    )
      return `未找到部署组件 ${requestedComponent.value}`;
    return "";
  });
  const visibleGroups = computed(() =>
    groups.value
      .filter(group => !requestedHost.value || group.host.host_id === requestedHost.value)
      .map(group => {
        const needle = search.value.trim().toLowerCase();
        const hostMatches = [
          group.host.host_id,
          group.host.label,
          group.host.address,
          group.host.private_address,
          group.host.region,
          group.host.description
        ]
          .join(" ")
          .toLowerCase()
          .includes(needle);
        return {
          ...group,
          rows: group.rows.filter(row => {
            const disabled = row.status === "disabled";
            if (filter.value === "disabled" && !disabled) return false;
            if ((filter.value === "enabled" || filter.value === "abnormal") && disabled) return false;
            if (filter.value === "abnormal" && !["down", "degraded", "unknown"].includes(row.status)) return false;
            return hostMatches || [row.deployment.component_id, row.definition?.name].join(" ").toLowerCase().includes(needle);
          })
        };
      })
      .filter(group => group.rows.length > 0)
  );
  const selectedGroup = computed(() => groups.value.find(group => group.host.host_id === selectedHostId.value));
  const detail = computed(() =>
    groups.value
      .flatMap(group => group.rows.map(row => ({ host: group.host, row })))
      .find(item => item.row.key === drawerKey.value)
  );
  const drawerVisible = computed({
    get: () => !!detail.value,
    set: (value: boolean) => {
      if (!value) drawerKey.value = "";
    }
  });
  const currentRoutes = computed(() => routes.value[selectedHostId.value]);
  const currentRouteError = computed(() => routeErrors.value[selectedHostId.value] || "");
  const gatewaySignal = (hostId: string, kind: string) =>
    monitorError.value || error.value
      ? undefined
      : overview.value?.hosts?.find(host => host.host_id === hostId)?.gateway_signals?.find(signal => signal.kind === kind);

  function applyLocator() {
    const requested = requestedHost.value;
    if (requested) selectedHostId.value = hosts.value.some(host => host.host_id === requested) ? requested : "";
    else if (!hosts.value.some(host => host.host_id === selectedHostId.value))
      selectedHostId.value = hosts.value[0]?.host_id || "";
    const locator = `${requested}/${requestedComponent.value}`;
    if (requestedComponent.value && appliedLocator !== locator) {
      const group = groups.value.find(
        item =>
          (!requested || item.host.host_id === requested) &&
          item.rows.some(row => row.deployment.component_id === requestedComponent.value)
      );
      drawerKey.value = group?.rows.find(row => row.deployment.component_id === requestedComponent.value)?.key || "";
      if (group) {
        appliedLocator = locator;
        // 定位的部署可能已停用：切到“全部”才看得见
        if (filter.value !== "all") filter.value = "all";
      }
    }
    if (!requestedComponent.value) appliedLocator = "";
  }
  watch([requestedHost, requestedComponent], applyLocator);
  async function selectRouteHost(value: string) {
    selectedHostId.value = value;
    drawerKey.value = "";
    const next = { ...route.query };
    next.tab = "routes";
    next.host_id = value;
    delete next.component_id;
    await router.replace({ query: next });
  }
  async function clearLocator() {
    const next = { ...route.query };
    delete next.host_id;
    delete next.component_id;
    drawerKey.value = "";
    await router.replace({ query: next });
  }
  /** background：后台轮询，不弹全局提示，失败写在页面上并保留上次的数据。 */
  async function refresh(background = false) {
    const request = guard.begin();
    loading.value = true;
    const options = { silentError: background };
    try {
      const [rawCatalog, hostRows, deploymentItems, health] = await Promise.all([
        getCatalog(options),
        listAllDeploymentHosts(options),
        listAllDeployments(options),
        monitorApi
          .getOverview({}, options)
          .then(result => ({ overview: result.overview, error: "" }))
          .catch(reason => ({ overview: undefined, error: reason instanceof Error ? reason.message : String(reason) }))
      ]);
      const definition = decodeCatalog(rawCatalog);
      const snapshots = await loadHostRoutes(hostRows, hostId => getHostRoutes(hostId, options));
      if (!request.isLatest()) return;
      catalog.value = definition;
      catalogHash.value = rawCatalog.sha256;
      controlHostId.value = rawCatalog.control_host_id;
      hosts.value = hostRows;
      deployments.value = deploymentItems;
      overview.value = health.overview;
      monitorError.value = health.error || (!health.overview?.topology_known ? "Monitor 尚未取得完整部署清单" : "");
      routes.value = snapshots.routes;
      routeErrors.value = snapshots.errors;
      loaded.value = true;
      error.value = "";
      applyLocator();
    } catch (reason) {
      if (request.isLatest()) error.value = reason instanceof Error ? reason.message : String(reason);
    } finally {
      if (request.isLatest()) loading.value = false;
    }
  }
  // 每 15 秒后台刷新一次（页面隐藏时暂停、不叠加）
  usePolling(() => refresh(true), 15_000);
  onMounted(() => void refresh());
  onBeforeUnmount(() => {
    guard.invalidate();
    loading.value = false;
  });
  return {
    tab,
    catalog,
    catalogHash,
    controlHostId,
    hosts,
    overview,
    unregistered,
    loading,
    loaded,
    error,
    monitorError,
    search,
    filter,
    selectedHostId,
    groups,
    visibleGroups,
    selectedGroup,
    currentRoutes,
    currentRouteError,
    routes,
    routeErrors,
    drawerKey,
    drawerVisible,
    detail,
    locatorError,
    requestedHost,
    requestedComponent,
    clearLocator,
    selectRouteHost,
    gatewaySignal,
    refresh
  };
}
