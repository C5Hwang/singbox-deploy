<script setup lang="ts">
import { computed, ref } from "vue";
import PeakAverageToggle from "./PeakAverageToggle.vue";
import { buildFrame, lineSeries, withPeakAverage, SOURCE_COLORS } from "../chartOptions";
import { fetchLatencySeries } from "../api";
import { tzOffsetMinutes } from "../timezone";
import { useTrendChart } from "../useTrendChart";
import type { LatencySnapshot, PingSeries, PingTarget } from "../types";

const props = defineProps<{ nodeKey: string; nodeName: string; snapshot: LatencySnapshot }>();
const emit = defineEmits<{ close: [] }>();

const showPeakAverage = ref(false);
const history = ref<PingSeries | null>(null);
const loadError = ref("");

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

// The card that opened this modal carries only the newest round, so the week is
// fetched here — once, on open, rather than on the page's minute poll.
async function load() {
  try {
    history.value = await fetchLatencySeries(props.nodeKey);
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e);
  }
}

// The grid the node sends is always a full week, one slot a minute, whether or
// not anything was recorded in each — that is what makes it a grid. The chart
// only draws the part of it that was: a node installed yesterday would
// otherwise get an axis six days of which are empty, which reads as a broken
// chart rather than as a young one.
//
// A round that answered nothing still counts as recorded, so an outage is
// inside the window as a gap rather than trimmed off the end of it. loss is
// what says a round happened; ms is null for the ones that answered nothing.
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

// Every round the node recorded, at the minute it recorded it. A slot with no
// value — a round that answered nothing, or a minute the monitor was not
// running — becomes a null, which draws as a gap rather than as zero latency.
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

// What the subtitle claims has to be what the axis shows, so it reports the
// span that was actually recorded rather than the week that was asked for.
const spanLabel = computed(() => {
  const series = history.value;
  const span = recorded.value;
  if (!series || !span) return "no rounds recorded yet";
  const hours = ((span[1] - span[0]) * series.step) / 3600;
  if (hours < 1) return "every minute · under an hour";
  if (hours < 48) return `every minute · last ${Math.round(hours)} h`;
  return `every minute · last ${Math.round(hours / 24)} days`;
});

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

function seriesName(target: PingTarget): string {
  return `${shortCarrier(target.carrier)} · ${target.city}`;
}

// The filter chips and the legend both carry the carrier, and "China" on every
// one of them is three words the reader already knows.
function shortCarrier(name: string): string {
  return name.replace(/^China\s+/, "");
}

function close() {
  emit("close");
}

const { chartRef, loading } = useTrendChart(
  load,
  buildOption,
  [tzOffsetMinutes, selectedCarriers, selectedCities],
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
          <p class="modal-subtitle">
            {{ shownTargets.length }} of {{ snapshot.targets.length }} probes · {{ spanLabel }}
          </p>
        </div>
        <div class="modal-controls">
          <PeakAverageToggle v-model="showPeakAverage" />
        </div>
      </div>

      <div class="filters">
        <div class="filter-group">
          <span class="eyebrow">Carrier</span>
          <button
            v-for="carrier in carriers"
            :key="carrier"
            type="button"
            class="check"
            :class="{ on: selectedCarriers.includes(carrier) }"
            :aria-pressed="selectedCarriers.includes(carrier)"
            @click="toggle('carrier', carrier)"
          >
            <i class="tick" aria-hidden="true"></i>{{ shortCarrier(carrier) }}
          </button>
        </div>
        <div class="filter-group">
          <span class="eyebrow">City</span>
          <button
            v-for="city in cities"
            :key="city"
            type="button"
            class="check"
            :class="{ on: selectedCities.includes(city) }"
            :aria-pressed="selectedCities.includes(city)"
            @click="toggle('city', city)"
          >
            <i class="tick" aria-hidden="true"></i>{{ city }}
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
/* A button with aria-pressed rather than a checkbox: the last chip in a group
   refuses to switch itself off, and a native checkbox that has already toggled
   itself in the DOM would be left contradicting the state that refused it. */
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
