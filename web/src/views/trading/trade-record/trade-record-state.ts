import { reactive } from "vue";

// Keep query and pagination state while the route shell recreates the view for a tab URL change.
export const tradeRecordViewState = reactive({
  filterSymbol: "",
  onlyOpen: false,
  orderState: "",
  orderTimeRange: [] as number[],
  fillTimeRange: [] as number[],
  orderPage: 1,
  fillPage: 1
});
