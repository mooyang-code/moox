import { ref } from "vue";
import { listAllDeploymentHosts, listDeployments } from "@/api/admin/sysdeploy";
import { ARCHIVE_WORKER_COMPONENT, resolveWorkerHost, type WorkerHost } from "./archive-model";

/**
 * 归档执行器所在主机：sysdeploy.ListDeployments（组件 archive-worker、已启用）加 sysdeploy.ListHosts，
 * 与运维“服务”“主机工作台”页用的是同一组接口；口径和 Admin 的归档转发一致（部署与主机都启用、只认唯一一条）。
 * 主机与空间无关，页面打开时读一次，失败时可重试。
 */
export function useArchiveWorkerHost() {
  const host = ref<WorkerHost | null>(null);
  const error = ref("");
  let seq = 0;

  async function load() {
    const token = ++seq;
    try {
      const [deployments, hosts] = await Promise.all([
        listDeployments(
          { component_id: ARCHIVE_WORKER_COMPONENT, status: "enabled", page: { page: 1, size: 100 } },
          { silentError: true }
        ),
        listAllDeploymentHosts({ silentError: true })
      ]);
      if (token !== seq) return;
      host.value = resolveWorkerHost(deployments.deployments || [], hosts);
      error.value = "";
    } catch (cause) {
      if (token !== seq) return;
      error.value = cause instanceof Error && cause.message ? cause.message : "请稍后重试";
    }
  }

  return { host, error, load };
}
