<template>
  <div class="moox-page">
    <div class="moox-inner">
      <a-alert class="host-list-hint" type="info" show-icon>
        主机的新增、部署、启停和移除只能通过 moox skill（moox-cli）操作，这里只读；已登记 SSH 的主机可以连接终端和管理文件。
      </a-alert>

      <a-table
        row-key="key"
        size="small"
        :loading="loading"
        :data="rows"
        :bordered="{ cell: true }"
        :pagination="false"
        :scroll="{ x: '100%', minWidth: 1200 }"
      >
        <template #columns>
          <a-table-column title="名称" :width="160">
            <template #cell="{ record }">
              <span class="host-name">{{ record.displayName }}</span>
            </template>
          </a-table-column>
          <a-table-column title="主机 ID" data-index="hostId" :width="120" />
          <a-table-column title="地址" data-index="displayAddress" :width="150" />
          <a-table-column title="内网地址" :width="140">
            <template #cell="{ record }">{{ record.host.private_address || "-" }}</template>
          </a-table-column>
          <a-table-column title="地域" :width="120">
            <template #cell="{ record }">{{ record.host.region || "-" }}</template>
          </a-table-column>
          <a-table-column title="可达性" :width="100" align="center">
            <template #cell="{ record }">{{ record.host.reachability === "outbound" ? "只能出站" : "可直达" }}</template>
          </a-table-column>
          <a-table-column title="SSH" :width="170">
            <template #cell="{ record }">{{ sshText(record) }}</template>
          </a-table-column>
          <a-table-column title="资源状态" :width="110" align="center">
            <template #cell="{ record }">
              <a-tag size="small" :color="stateColor(record)">{{ stateLabel(record) }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="操作" :width="200" align="center" fixed="right">
            <template #cell="{ record }">
              <a-space>
                <a-tooltip :content="sshAvailability(record.host).reason" :disabled="sshAvailability(record.host).ok">
                  <a-link :disabled="!sshAvailability(record.host).ok" @click="onConnect(record)">连接</a-link>
                </a-tooltip>
                <a-tooltip :content="sshAvailability(record.host).reason" :disabled="sshAvailability(record.host).ok">
                  <a-link :disabled="!sshAvailability(record.host).ok" @click="onFileManage(record)">文件管理</a-link>
                </a-tooltip>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { sshAvailability } from "@/views/ops/host-workbench/host-workbench-utils";
import type { HostMonitorRow } from "@/views/ops/host-workbench/host-monitor-mapping";

defineProps<{ rows: HostMonitorRow[]; loading?: boolean }>();
const emit = defineEmits<{ connect: [hostId: string]; fileManage: [hostId: string] }>();

const sshText = (row: HostMonitorRow) =>
  sshAvailability(row.host).ok ? `${row.host.ssh_user || "-"}@:${row.host.ssh_port}` : "未登记";
const stateColor = (row: HostMonitorRow) => (row.state === "online" ? "green" : row.state === "offline" ? "red" : "gray");
const stateLabel = (row: HostMonitorRow) => (row.state === "online" ? "在线" : row.state === "offline" ? "离线" : "未上报");

// 置灰的链接不会触发 click，这里再守一次，避免键盘或外部调用绕过。
const onConnect = (row: HostMonitorRow) => {
  if (sshAvailability(row.host).ok) emit("connect", row.hostId);
};
const onFileManage = (row: HostMonitorRow) => {
  if (sshAvailability(row.host).ok) emit("fileManage", row.hostId);
};
</script>

<style lang="scss" scoped>
.moox-page {
  .moox-inner {
    display: flex;
    flex-direction: column;
    min-height: 100%;
  }

  .host-list-hint {
    margin-bottom: var(--moox-space-2);
  }

  .host-name {
    font-weight: 500;
    color: var(--color-text-1);
  }
}
</style>
