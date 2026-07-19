import { computed, ref } from "vue";

export function useViewCatalog<T extends { view_id?: string }>() {
  const views = ref<T[]>([]);
  const activeViewId = ref("");
  const activeView = computed(() => views.value.find(view => view.view_id === activeViewId.value));
  function replaceViews(nextViews: T[]) {
    views.value = nextViews;
    if (!nextViews.some(view => view.view_id === activeViewId.value)) activeViewId.value = nextViews[0]?.view_id || "";
  }
  return { views, activeViewId, activeView, replaceViews };
}
