<script setup lang="ts">
import { computed, ref } from "vue";
import TrendShell from "./TrendShell.vue";
import { fetchTrafficTrend, fetchTrafficRecent } from "../api";
import { formatBytes } from "../utils";
import {
  aggregateTrafficDaily,
  buildTrendOption,
  bytesAxis,
  trafficSeries,
  TRAFFIC_LEGEND,
  type TrafficPoint,
} from "../chartOptions";
import { modeShape, TRAFFIC_MODES, type TrendMode } from "../trendModes";
import { tzOffsetMinutes } from "../timezone";
import { useTrendChart } from "../useTrendChart";
import type { SourceSummary, HourlyPoint, TrafficRawPoint } from "../types";

const props = defineProps<{ source: SourceSummary }>();
const emit = defineEmits<{ close: [] }>();

const mode = ref<TrendMode>("hourly");
const showPeakAverage = ref(false);
const trend = ref<HourlyPoint[]>([]);
const recentPoints = ref<TrafficRawPoint[]>([]);

async function load() {
  try {
    const [trendData, recentData] = await Promise.all([
      fetchTrafficTrend(props.source.id || props.source.name),
      fetchTrafficRecent(props.source.id || props.source.name),
    ]);
    trend.value = trendData;
    recentPoints.value = recentData;
  } catch {
    trend.value = [];
    recentPoints.value = [];
  }
}

// The buckets are timestamped by the hour or day they opened and the raw
// samples by themselves; past here they are all just points in time.
const points = computed<TrafficPoint[]>(() => {
  const { isRecent, isDaily } = modeShape(mode.value);
  if (isRecent) return recentPoints.value;
  const buckets = isDaily ? aggregateTrafficDaily(trend.value) : trend.value;
  return buckets.map((p) => ({ ts: p.hourTs, inBytes: p.inBytes, outBytes: p.outBytes, totalBytes: p.totalBytes }));
});

function buildOption(): any {
  const { isRecent, unit, tooltipUnit } = modeShape(mode.value);
  return buildTrendOption({
    el: chartRef.value,
    unit,
    tooltipUnit,
    legend: TRAFFIC_LEGEND,
    tooltipValue: (p) => formatBytes(Array.isArray(p.value) ? p.value[1] : p.value),
    yAxis: bytesAxis,
    // A marker on every point is a smear once the points are seconds apart, so
    // only the bucketed views carry them, and only where there is room.
    series: (narrow) => trafficSeries(points.value, !narrow && !isRecent),
    peakAverage: { show: showPeakAverage.value, format: formatBytes },
  });
}

function close() {
  emit("close");
}

const { chartRef, loading } = useTrendChart(load, buildOption, [mode, tzOffsetMinutes], close, [showPeakAverage]);
</script>

<template>
  <TrendShell
    :title="source.name"
    subtitle="Traffic Trend"
    :modes="TRAFFIC_MODES"
    v-model:mode="mode"
    v-model:peakAverage="showPeakAverage"
    :loading="loading"
    @close="close"
  >
    <div ref="chartRef" class="chart-container"></div>
  </TrendShell>
</template>
