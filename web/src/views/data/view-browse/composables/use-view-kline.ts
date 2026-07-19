import { ref } from "vue";

export function useViewKline() {
  const visible = ref(false);
  const loading = ref(false);
  function open() {
    visible.value = true;
  }
  function close() {
    visible.value = false;
    loading.value = false;
  }
  return { visible, loading, open, close };
}
