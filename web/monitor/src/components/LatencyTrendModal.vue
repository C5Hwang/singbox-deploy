<script setup lang="ts">
import { computed, ref } from "vue";
import PeakAverageToggle from "./PeakAverageToggle.vue";
import { buildFrame, lineSeries, withPeakAverage, SOURCE_COLORS, type TimeUnit } from "../chartOptions";
import { tzOffsetMinutes } from "../timezone";
import { useTrendChart } from "../useTrendChart";
import type { LatencySnapshot, PingTarget } from "../types";

const props = defineProps<{ nodeName: string; snapshot: LatencySnapshot }>();
const emit = defineEmits<{ close: [] }>();

type Granularity = "recent" | "hourly" | "daily";
const granularity = ref<Granularity>("hourly");
const showPeakAverage = ref(false);

// Carriers and cities are filtered independently: the interesting comparisons
// are one carrier across three cities and one city across three carriers, and a
// single flat list of nine lines cannot express either.
const carriers = computed(() => uniqueBy((t) => t.carrier));
const cities = computed(() => uniqueBy((t) => t.city));

function uniqueBy(pick: (t: PingTarget) => string): string[] {
  const seen: string[] = [];
  for (const target of props.snapshot.targets) {
    const value = pick(target);
    if (!seen.includes(value)) seen.push(value);
  }
  return seen;
}

const selectedCarriers = ref<string[]>([...carriers.value]);
const selectedCities = ref<string[]>([...cities.value]);

// Clearing the last box would leave an empty chart with no way back other than
// guessing which box to tick, so the last one in a group stays on.
function toggle(group: "carrier" | "city", value: string) {
  const list = group === "carrier" ? selectedCarriers : selectedCities;
  if (list.value.includes(value)) {
    if (list.value.length === 1) return;
    list.value = list.value.filter((v) => v !== value);
  } else {
    list.value = [...list.value, value];
  }
}

const shownTargets = computed(() =>
  props.snapshot.targets.filter(
    (t) => selectedCarriers.value.includes(t.carrier) && selectedCities.value.includes(t.city),
  ),
);

async function load() {
  // The snapshot arrives with the card that opened this modal; nothing to fetch.
}

function buildOption(): any {
  const isRecent = granularity.value === "recent";
  const isDaily = granularity.value === "daily";
  const unit: TimeUnit = isDaily ? "day" : "hour";
  const targets = shownTargets.value;

  const { narrow, option } = buildFrame({
    width: chartRef.value?.clientWidth ?? 800,
    unit,
    legend: targets.map(seriesName),
    sortTooltip: true,
    tooltipUnit: isRecent ? "second" : unit,
    tooltipValue: (p) => {
      const value = Number(Array.isArray(p.value) ? p.value[1] : p.value);
      return Number.isFinite(value) ? `${value.toFixed(1)} ms` : "NA";
    },
  });

  const series = targets.map((target, i) => {
    // A fully lost round has no latency; feeding null leaves a gap in the line
    // instead of dropping the series to the axis.
    const data: [number, number][] = isRecent
      ? props.snapshot.recent.filter((p) => p.target === target.id).map((p) => [p.ts * 1000, p.avgMs as number])
      : (isDaily ? props.snapshot.daily : props.snapshot.points)
          .filter((p) => p.target === target.id)
          .map((p) => [p.hourTs * 1000, p.avgMs as number]);
    return lineSeries(seriesName(target), SOURCE_COLORS[i % SOURCE_COLORS.length], data, { showSymbol: !narrow && !isRecent });
  });

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
      format: (v: number) => `${v.toFixed(0)} ms`,
      narrow,
    }),
  };
}

function seriesName(target: PingTarget): string {
  return `${target.carrier} · ${target.city}`;
}

function close() {
  emit("close");
}

const { chartRef, loading } = useTrendChart(
  load,
  buildOption,
  [granularity, tzOffsetMinutes, selectedCarriers, selectedCities],
  close,
  [showPeakAverage],
);
</script>

<template>
  <div class="modal-backdrop" @click.self="close">
    <div class="modal-content">
      <button class="close-btn" @click="close" aria-label="Close">&times;</button>
      <div class="modal-header">
        <div>
          <h2 class="modal-title">{{ nodeName }}</h2>
          <p class="modal-subtitle">Latency to {{ shownTargets.length }} of {{ snapshot.targets.length }} probes</p>
        </div>
        <div class="modal-controls">
          <div class="toggle-group">
            <button :class="{ active: granularity === 'recent' }" @click="granularity = 'recent'">Recent</button>
            <button :class="{ active: granularity === 'hourly' }" @click="granularity = 'hourly'">Hourly</button>
            <button :class="{ active: granularity === 'daily' }" @click="granularity = 'daily'">Daily</button>
          </div>
          <PeakAverageToggle v-model="showPeakAverage" />
        </div>
      </div>

      <div class="filters">
        <div class="filter-group">
          <span class="eyebrow">Carrier</span>
          <label v-for="carrier in carriers" :key="carrier" class="check">
            <input type="checkbox" :checked="selectedCarriers.includes(carrier)" @change="toggle('carrier', carrier)" />
            <span>{{ carrier }}</span>
          </label>
        </div>
        <div class="filter-group">
          <span class="eyebrow">City</span>
          <label v-for="city in cities" :key="city" class="check">
            <input type="checkbox" :checked="selectedCities.includes(city)" @change="toggle('city', city)" />
            <span>{{ city }}</span>
          </label>
        </div>
      </div>

      <div v-if="loading" class="chart-loading">Loading latency data...</div>
      <div v-show="!loading" ref="chartRef" class="chart-container"></div>
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
.check {
  display: inline-flex; align-items: center; gap: 7px;
  font-size: 13px; font-weight: 650; color: var(--text); cursor: pointer;
  padding: 5px 10px; border: 1px solid var(--line); border-radius: 999px;
  background: white; transition: background 0.15s, border-color 0.15s, color 0.15s;
}
.check:hover { background: #f6f9fd; }
.check:has(input:checked) {
  background: #edf4ff; color: var(--blue);
  border-color: color-mix(in srgb, var(--blue), transparent 60%);
}
.check input { accent-color: var(--blue); width: 14px; height: 14px; cursor: pointer; }
@media (max-width: 720px) {
  .filters { padding: 4px 16px 10px; gap: 8px 16px; }
  .check { font-size: 12px; padding: 4px 8px; }
}
</style>
