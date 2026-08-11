import { nextTick, onMounted, onUnmounted, ref, shallowRef, watch, type WatchSource } from "vue";
import * as echarts from "echarts/core";
import { LineChart } from "echarts/charts";
import {
  GridComponent,
  TooltipComponent,
  LegendComponent,
  DataZoomComponent,
  MarkLineComponent,
  MarkPointComponent,
} from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";

echarts.use([
  LineChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  DataZoomComponent,
  MarkLineComponent,
  MarkPointComponent,
  CanvasRenderer,
]);

// Shared lifecycle for the modal trend charts: load data, init ECharts,
// rebuild on the given reactive sources, debounce resize, close on Escape.
//
// mergeOn is for changes that annotate the chart rather than replace it — the
// peak/average overlay. Those update by merging into the live option, so the
// curves stay exactly where they are and only the annotations animate; a
// rebuild would redraw every line for what is meant to read as a light touch.
//
// holdAnnotations delays the peak/average marks past the curve animation on a
// full render. An annotation is a statement about a shape, and on startup the
// shape is still being drawn: the average line arrives at its final height
// immediately while the curve is still sweeping in under it, so the two cross
// each other over and over for the length of the sweep. Waiting until the
// curves have settled costs nothing — the overlay is a light touch either way —
// and it never applies to a merge, where there is no sweep to wait for.
function holdAnnotations(option: Record<string, any>): Record<string, any> {
  const delay = Number(option.animationDuration) || 0;
  if (delay <= 0 || !Array.isArray(option.series)) return option;
  return {
    ...option,
    series: option.series.map((s: any) =>
      s?.markLine || s?.markPoint
        ? {
            ...s,
            ...(s.markLine ? { markLine: { ...s.markLine, animationDelay: delay } } : {}),
            ...(s.markPoint ? { markPoint: { ...s.markPoint, animationDelay: delay } } : {}),
          }
        : s,
    ),
  };
}
export function useTrendChart(
  load: () => Promise<void>,
  buildOption: () => Record<string, any>,
  rebuildOn: WatchSource[],
  close: () => void,
  mergeOn: WatchSource[] = [],
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
      chart.value.setOption(holdAnnotations(buildOption()));
      resizeHandler = () => {
        if (resizeTimer) window.clearTimeout(resizeTimer);
        resizeTimer = window.setTimeout(() => {
          chart.value?.resize();
          chart.value?.setOption(holdAnnotations(buildOption()), true);
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
    chart.value?.setOption(holdAnnotations(buildOption()), true);
  });

  if (mergeOn.length > 0) {
    watch(mergeOn, () => {
      chart.value?.setOption(buildOption(), false);
    });
  }

  return { chartRef, loading };
}
