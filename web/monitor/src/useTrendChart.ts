import { nextTick, onMounted, onUnmounted, ref, shallowRef, watch, type WatchSource } from "vue";
import * as echarts from "echarts/core";
import { LineChart } from "echarts/charts";
import { GridComponent, TooltipComponent, LegendComponent, DataZoomComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, DataZoomComponent, CanvasRenderer]);

// Shared lifecycle for the modal trend charts: load data, init ECharts,
// rebuild on the given reactive sources, debounce resize, close on Escape.
export function useTrendChart(
  load: () => Promise<void>,
  buildOption: () => Record<string, any>,
  rebuildOn: WatchSource[],
  close: () => void,
) {
  const chartRef = ref<HTMLDivElement>();
  const chart = shallowRef<echarts.ECharts>();
  const loading = ref(true);
  let resizeHandler: (() => void) | undefined;
  let resizeTimer: number | undefined;
  let keyHandler: ((e: KeyboardEvent) => void) | undefined;

  onMounted(async () => {
    keyHandler = (e) => {
      if (e.key === "Escape") close();
    };
    window.addEventListener("keydown", keyHandler);
    await load();
    loading.value = false;
    await nextTick();
    if (chartRef.value) {
      chart.value = echarts.init(chartRef.value);
      chart.value.setOption(buildOption());
      resizeHandler = () => {
        if (resizeTimer) window.clearTimeout(resizeTimer);
        resizeTimer = window.setTimeout(() => {
          chart.value?.resize();
          chart.value?.setOption(buildOption(), true);
        }, 120);
      };
      window.addEventListener("resize", resizeHandler);
    }
  });

  onUnmounted(() => {
    if (resizeHandler) window.removeEventListener("resize", resizeHandler);
    if (keyHandler) window.removeEventListener("keydown", keyHandler);
    if (resizeTimer) window.clearTimeout(resizeTimer);
    chart.value?.dispose();
  });

  watch(rebuildOn, () => {
    chart.value?.setOption(buildOption(), true);
  });

  return { chartRef, loading };
}
