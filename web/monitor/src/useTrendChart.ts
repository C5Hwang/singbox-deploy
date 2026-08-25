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
// rebuildOn redraws the chart from a fresh option, and the zoom window the
// operator has dragged out survives that redraw: the stretch of time they are
// looking at is theirs, not the option's. resetOn is for the changes that make
// that window meaningless — a granularity switch draws a different stretch of
// time altogether, so it starts back at the whole of it.
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

// The window the operator has dragged the zoom slider to, as the two
// timestamps it spans, or null while they are still looking at the whole span.
//
// A rebuild hands ECharts a fresh option, and a fresh option carries the
// default window — all of it. So every rebuild used to throw away the hour
// someone had picked out: ticking a carrier box, switching timezone or just
// resizing the browser all snapped the chart back to the full week.
//
// The window travels as timestamps rather than as the percentages the slider
// is dragged in, because a rebuild can change what the span is — dropping the
// one probe that recorded earliest moves the left edge of the axis — and what
// was asked for is a stretch of time, not the middle third of whatever happens
// to be drawn.
function zoomWindow(chart: echarts.ECharts): [number, number] | null {
  const zoom = ((chart.getOption() as any)?.dataZoom ?? [])[0];
  if (!zoom) return null;
  // A slider still at both ends is not a window, and pinning the rebuilt chart
  // to the old span would hide whatever the rebuild has just added to it.
  if (!(zoom.start > 0 || zoom.end < 100)) return null;
  const start = Number(zoom.startValue);
  const end = Number(zoom.endValue);
  return Number.isFinite(start) && Number.isFinite(end) ? [start, end] : null;
}

// Both zooms — the slider and the wheel — read the same window, so both are
// told about it. A window the redrawn data no longer covers is clamped to what
// it does cover by ECharts itself.
function withZoomWindow(
  option: Record<string, any>,
  window: [number, number] | null,
): Record<string, any> {
  if (!window || !Array.isArray(option.dataZoom)) return option;
  return {
    ...option,
    dataZoom: option.dataZoom.map((zoom: any) => ({
      ...zoom,
      startValue: window[0],
      endValue: window[1],
    })),
  };
}

export function useTrendChart(
  load: () => Promise<void>,
  buildOption: () => Record<string, any>,
  rebuildOn: WatchSource[],
  close: () => void,
  mergeOn: WatchSource[] = [],
  resetOn: WatchSource[] = [],
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
          rebuild(true);
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

  function rebuild(keepWindow: boolean) {
    const instance = chart.value;
    if (!instance) return;
    instance.setOption(
      withZoomWindow(holdAnnotations(buildOption()), keepWindow ? zoomWindow(instance) : null),
      true,
    );
  }

  watch(rebuildOn, () => rebuild(true));

  if (resetOn.length > 0) {
    watch(resetOn, () => rebuild(false));
  }

  // A merge leaves the zoom components untouched, so the window rides through
  // one on its own — nothing to carry over here.
  if (mergeOn.length > 0) {
    watch(mergeOn, () => {
      chart.value?.setOption(buildOption(), false);
    });
  }

  return { chartRef, loading };
}
