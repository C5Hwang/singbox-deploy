<script setup lang="ts">
import { computed, ref } from "vue";
import PeakAverageToggle from "./PeakAverageToggle.vue";
import { buildFrame, lineSeries, withPeakAverage, SOURCE_COLORS } from "../chartOptions";
import { fetchLatencySeries } from "../api";
import { tzOffsetMinutes } from "../timezone";
import { useTrendChart } from "../useTrendChart";
import { relayTargets, type LatencySnapshot, type PingSeries, type PingTarget } from "../types";

// The week of relay-to-landing rounds for one relay. The pairs are a flat list
// — one landing node each — so unlike the carrier chart there is one filter
// group rather than two.
const props = defineProps<{ nodeKey: string; nodeName: string; snapshot: LatencySnapshot }>();
const emit = defineEmits<{ close: [] }>();

const showPeakAverage = ref(false);
const history = ref<PingSeries | null>(null);
const loadError = ref("");

const allTargets = computed(() => relayTargets(props.snapshot.targets));
const selected = ref<string[]>(allTargets.value.map((t) => t.id));

// Clearing the last box would leave an empty chart with no way back other than
// guessing which box to tick, so the last one stays on.
function toggle(id: string) {
  if (selected.value.includes(id)) {
    if (selected.value.length === 1) return;
    selected.value = selected.value.filter((v) => v !== id);
  } else {
    selected.value = [...selected.value, id];
  }
}

const shownTargets = computed(() => allTargets.value.filter((t) => selected.value.includes(t.id)));

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
  if (!series || !span) return "no rounds recorded yet";
  const hours = ((span[1] - span[0]) * series.step) / 3600;
  if (hours < 1) return "every minute · under an hour";
  if (hours < 48) return `every minute · last ${Math.round(hours)} h`;
  return `every minute · last ${Math.round(hours / 24)} days`;
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

const { chartRef, loading } = useTrendChart(load, buildOption, [tzOffsetMinutes, selected], close, [showPeakAverage]);
</script>

<template>
  <div class="modal-backdrop" @click.self="close">
    <div class="modal-content">
      <button class="close-btn" @click="close" aria-label="Close">&times;</button>
      <div class="modal-header">
        <div>
          <h2 class="modal-title">{{ nodeName }}</h2>
          <p class="modal-subtitle">
            {{ shownTargets.length }} of {{ allTargets.length }} landing nodes · {{ spanLabel }}
          </p>
        </div>
        <div class="modal-controls">
          <PeakAverageToggle v-model="showPeakAverage" />
        </div>
      </div>

      <div class="filters">
        <div class="filter-group">
          <span class="eyebrow">Landing node</span>
          <button
            v-for="target in allTargets"
            :key="target.id"
            type="button"
            class="check"
            :class="{ on: selected.includes(target.id) }"
            :aria-pressed="selected.includes(target.id)"
            @click="toggle(target.id)"
          >
            <i class="tick" aria-hidden="true"></i>{{ seriesName(target) }}
          </button>
        </div>
      </div>

      <div v-if="loading" class="chart-loading">Loading latency data...</div>
      <div v-else-if="loadError" class="chart-loading">Latency history is unavailable: {{ loadError }}.</div>
      <div v-show="!loading && !loadError" ref="chartRef" class="chart-container"></div>
    </div>
  </div>
</template>

<style scoped>
.filters {
  display: flex; flex-wrap: wrap; gap: 10px 28px;
  padding: 4px 28px 12px; border-bottom: 1px solid var(--line);
}
.filter-group { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 14px; }
.filter-group .eyebrow { margin: 0 2px 0 0; }
/* A button with aria-pressed rather than a checkbox: the last chip refuses to
   switch itself off, and a native checkbox that has already toggled itself in
   the DOM would be left contradicting the state that refused it. */
.check {
  display: inline-flex; align-items: center; gap: 7px;
  font: inherit; font-size: 13px; font-weight: 650; color: var(--muted); cursor: pointer;
  padding: 5px 11px 5px 8px; border: 1px solid var(--line); border-radius: 999px;
  background: white; transition: background 0.15s, border-color 0.15s, color 0.15s;
}
.check:hover { background: #f6f9fd; color: var(--text); }
.check .tick {
  width: 14px; height: 14px; border-radius: 5px; flex-shrink: 0;
  border: 1.5px solid var(--line); background: white;
  transition: background 0.15s, border-color 0.15s;
}
.check.on {
  background: #edf4ff; color: var(--blue);
  border-color: color-mix(in srgb, var(--blue), transparent 60%);
}
.check.on .tick {
  background: var(--blue); border-color: var(--blue);
  background-image: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 12 12'><path d='M2.5 6.2l2.3 2.3 4.7-4.9' fill='none' stroke='white' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'/></svg>");
  background-size: 12px 12px; background-position: center; background-repeat: no-repeat;
}
@media (max-width: 720px) {
  .filters { padding: 4px 16px 10px; gap: 8px 16px; }
  .check { font-size: 12px; padding: 4px 8px; }
}
</style>
