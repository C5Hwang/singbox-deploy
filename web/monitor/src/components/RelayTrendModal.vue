<script setup lang="ts">
import { computed, ref } from "vue";
import PeakAverageToggle from "./PeakAverageToggle.vue";
import { buildFrame, lineSeries, withPeakAverage, SOURCE_COLORS } from "../chartOptions";
import { fetchLatencySeries } from "../api";
import { tzOffsetMinutes } from "../timezone";
import { useTrendChart } from "../useTrendChart";
import { relayTargets, type LatencySnapshot, type PingSeries, type PingTarget } from "../types";

// The week of relay-to-landing rounds for one relay, one line per landing node.
const props = defineProps<{ nodeKey: string; nodeName: string; snapshot: LatencySnapshot }>();
const emit = defineEmits<{ close: [] }>();

const showPeakAverage = ref(false);
const history = ref<PingSeries | null>(null);
const loadError = ref("");

// Every landing node is drawn; the chart's own legend is what turns one off,
// so a second row of chips above it would only be the same control twice.
const shownTargets = computed(() => relayTargets(props.snapshot.targets));

async function load() {
  try {
    history.value = await fetchLatencySeries(props.nodeKey);
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e);
  }
}

// The node sends a full week of one-minute slots whether or not anything was
// recorded in each. Only the recorded part is drawn: a link created yesterday
// would otherwise get an axis six days of which are empty, which reads as a
// broken chart rather than as a young one. loss is what says a round happened.
const recorded = computed<[number, number] | null>(() => {
  const series = history.value;
  if (!series) return null;
  let first = -1;
  let last = -1;
  for (const target of shownTargets.value) {
    const track = series.series[target.id];
    if (!track) continue;
    for (let i = 0; i < track.loss.length; i++) {
      if (track.loss[i] < 0) continue;
      if (first < 0 || i < first) first = i;
      if (i > last) last = i;
    }
  }
  return first < 0 ? null : [first, last];
});

function trackData(targetId: string): [number, number | null][] {
  const series = history.value;
  const track = series?.series[targetId];
  const span = recorded.value;
  if (!series || !track || !span) return [];
  const points: [number, number | null][] = [];
  for (let i = span[0]; i <= span[1]; i++) {
    points.push([(series.start + i * series.step) * 1000, track.ms[i]]);
  }
  return points;
}

const spanLabel = computed(() => {
  const series = history.value;
  const span = recorded.value;
  if (!series || !span) return "no rounds yet";
  const hours = ((span[1] - span[0]) * series.step) / 3600;
  if (hours < 1) return "under an hour";
  if (hours < 48) return `last ${Math.round(hours)} h`;
  return `last ${Math.round(hours / 24)} days`;
});

function seriesName(target: PingTarget): string {
  return target.name || target.id;
}

function buildOption(): any {
  const targets = shownTargets.value;

  const { narrow, plotHeight, option } = buildFrame({
    width: chartRef.value?.clientWidth ?? 800,
    height: chartRef.value?.clientHeight ?? 0,
    unit: "minute",
    legend: targets.map(seriesName),
    sortTooltip: true,
    tooltipUnit: "minute",
    tooltipValue: (p) => {
      const value = Number(Array.isArray(p.value) ? p.value[1] : p.value);
      return Number.isFinite(value) ? `${value.toFixed(1)} ms` : "NA";
    },
  });

  const series = targets.map((target, i) =>
    lineSeries(seriesName(target), SOURCE_COLORS[i % SOURCE_COLORS.length], trackData(target.id), { dense: true }),
  );

  return {
    ...option,
    yAxis: {
      type: "value",
      name: narrow ? "" : "ms",
      min: 0,
      axisLine: { show: false },
      splitLine: { lineStyle: { color: "#f0f4f8" } },
      axisLabel: { color: "#7a869a", fontSize: narrow ? 10 : 12, formatter: (v: number) => `${v}` },
    },
    series: withPeakAverage(series, {
      show: showPeakAverage.value,
      plotHeight,
      format: (v: number) => `${v.toFixed(0)} ms`,
      narrow,
    }),
  };
}

function close() {
  emit("close");
}

const { chartRef, loading } = useTrendChart(load, buildOption, [tzOffsetMinutes], close, [showPeakAverage]);
</script>

<template>
  <div class="modal-backdrop" @click.self="close">
    <div class="modal-content">
      <button class="close-btn" @click="close" aria-label="Close">&times;</button>
      <div class="modal-header">
        <div>
          <h2 class="modal-title">{{ nodeName }}</h2>
          <p class="modal-subtitle">Landing nodes · {{ spanLabel }}</p>
        </div>
        <div class="modal-controls">
          <PeakAverageToggle v-model="showPeakAverage" />
        </div>
      </div>

      <div v-if="loading" class="chart-loading">Loading latency data...</div>
      <div v-else-if="loadError" class="chart-loading">Latency history is unavailable: {{ loadError }}.</div>
      <div v-show="!loading && !loadError" ref="chartRef" class="chart-container"></div>
    </div>
  </div>
</template>

<style scoped>
</style>
