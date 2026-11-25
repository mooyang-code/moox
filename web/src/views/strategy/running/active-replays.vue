<template>
  <p v-if="replayError" class="active-replays is-error">进行中的回放读取失败：{{ replayError }}</p>
  <p v-else-if="running.length" class="active-replays">
    <span class="active-replays__label">进行中的回放</span>
    <span v-for="replay in running" :key="replay.replay_id" class="active-replays__item">
      <a-link @click="go(replay.replay_id)">{{ replaySourceLabel(replay, store.strategyCatalog) }}</a-link>
      <span class="active-replays__text"
        >{{ replayStatusLabel(replay.status) }} · {{ replayRangeLabel(replay, displayZone)
        }}<template v-if="toInstantMs(replay.progress_time) !== null">
          · 进度 {{ formatInstant(replay.progress_time, displayZone) }}</template
        ></span
      >
    </span>
  </p>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { StaleSpaceRequestError } from "@/api/admin/stale-space";
import { listReplays } from "@/api/strategy";
import type { Replay } from "@/api/strategy-types";
import { useDisplayZone } from "@/hooks/useDisplayZone";
import { usePolling } from "@/hooks/usePolling";
import { useSpaceStore } from "@/store/modules/space";
import { useStrategyStore } from "@/store/modules/strategy";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { formatInstant, toInstantMs } from "@/utils/time-zone";
import { activeReplays } from "@/views/strategy/attention-model";
import { replayLink } from "@/views/strategy/links";
import { replayRangeLabel, replaySourceLabel, replayStatusLabel } from "@/views/strategy/replay/model";

// 运行实例列表顶部的一行：排队或运行中的回放，每 10 秒刷新，没有时不占位置。
const router = useRouter();
const store = useStrategyStore();
const spaceStore = useSpaceStore();
const displayZone = useDisplayZone();
const replays = ref<Replay[]>([]);
const replayError = ref("");
const gate = createLatestRequestGuard();
const running = computed(() => activeReplays(replays.value));

async function load() {
  const token = gate.begin();
  try {
    const page = await listReplays({ page: 1, page_size: 20 });
    if (!token.isLatest()) return;
    replays.value = page.items;
    replayError.value = "";
  } catch (error) {
    if (!token.isLatest() || error instanceof StaleSpaceRequestError) return;
    replayError.value = error instanceof Error && error.message ? error.message : "请稍后重试";
  }
}

function go(replayId: string) {
  void router.push(replayLink({ replayId }));
}

watch(
  () => spaceStore.selectedSpaceId,
  spaceId => {
    replays.value = [];
    replayError.value = "";
    if (spaceId) void load();
  },
  { immediate: true }
);
usePolling(load, 10_000, () => Boolean(spaceStore.selectedSpaceId));
</script>

<style scoped lang="scss">
.active-replays {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: var(--moox-space-2) var(--moox-space-4);
  margin: 0 0 var(--moox-space-3);
  font-size: 13px;
}

.active-replays.is-error {
  color: rgb(var(--warning-6));
}

.active-replays__label {
  color: var(--color-text-2);
  font-weight: 600;
}

.active-replays__item {
  display: inline-flex;
  gap: var(--moox-space-1);
}

.active-replays__text {
  color: var(--color-text-3);
}
</style>
