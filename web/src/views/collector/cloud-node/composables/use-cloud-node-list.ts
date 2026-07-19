import { computed, ref } from "vue";

export function useCloudNodeList<T extends { node_id?: string }>() {
  const rows = ref<T[]>([]);
  const loading = ref(false);
  const selectedKeys = ref<string[]>([]);
  const selectedRows = computed(() => rows.value.filter(row => selectedKeys.value.includes(row.node_id || "")));

  function replaceRows(nextRows: T[]) {
    rows.value = nextRows;
    const visible = new Set(nextRows.map(row => row.node_id).filter(Boolean));
    selectedKeys.value = selectedKeys.value.filter(key => visible.has(key));
  }

  function toggleSelection(keys: string[]) {
    selectedKeys.value = [...new Set(keys)];
  }

  return { rows, loading, selectedKeys, selectedRows, replaceRows, toggleSelection };
}
