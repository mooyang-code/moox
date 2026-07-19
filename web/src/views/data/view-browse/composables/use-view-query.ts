import { ref } from "vue";

export function useViewQuery<T>() {
  const rows = ref<T[]>([]);
  const loading = ref(false);
  const error = ref("");
  async function run(query: () => Promise<T[]>) {
    loading.value = true;
    error.value = "";
    try {
      rows.value = await query();
      return rows.value;
    } catch (cause) {
      error.value = cause instanceof Error ? cause.message : String(cause);
      throw cause;
    } finally {
      loading.value = false;
    }
  }
  return { rows, loading, error, run };
}
