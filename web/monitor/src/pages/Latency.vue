<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import InlineChart from "../components/InlineChart.vue";
import { fetchLatency } from "../api";
import { buildFrame, lineSeries, SOURCE_COLORS } from "../chartOptions";
import { tzOffsetMinutes } from "../timezone";
import { formatDateTime } from "../utils";
import type { LatencySnapshot, PingTarget, Summary } from "../types";

const props = defineProps<{ summary: Summary | null }>();

// A single-node install reports no source list; it is still one selectable
// node, spelled the way the traffic page spells it.
const sources = computed(() => {
  const list = props.summary?.sources ?? [];
  if (list.length > 0) return list;
  return props.summary ? [{ id: "local", name: "Local Server" }] : [];
});
const selected = ref<string>("");
const snapshot = ref<LatencySnapshot | null>(null);
const loadError = ref("");
const loading = ref(false);

// The selector holds a stable source key; the first source is picked once the
// summary arrives and kept unless that node disappears.
const selectedKey = computed(() => selected.value || sourceKey(sources.value[0]));

function sourceKey(source: { id?: string; name?: string } | undefined): string {
  return source ? source.id || source.name || "" : "";
}

async function load() {
  const key = selectedKey.value;
  if (!key) return;
  loading.value = true;
  try {
    snapshot.value = await fetchLatency(key);
    loadError.value = "";
  } catch (e) {
    snapshot.value = null;
    loadError.value = e instanceof Error ? e.message : String(e);
  } finally {
    loading.value = false;
  }
}

watch(selectedKey, load, { immediate: true });

// Probes run every five minutes; refreshing a little faster keeps the newest
// round on screen without polling a node harder than it samples.
let refreshTimer: number | undefined;
onMounted(() => {
  refreshTimer = window.setInterval(load, 60000);
});
onUnmounted(() => {
  if (refreshTimer) window.clearInterval(refreshTimer);
});

const carriers = computed<string[]>(() => {
  const seen: string[] = [];
  for (const target of snapshot.value?.targets ?? []) {
    if (!seen.includes(target.carrier)) seen.push(target.carrier);
  }
  return seen;
});

function targetsFor(carrier: string): PingTarget[] {
  return (snapshot.value?.targets ?? []).filter((t) => t.carrier === carrier);
}

function latestFor(targetID: string) {
  return snapshot.value?.latest.find((p) => p.target === targetID);
}

function latencyText(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return "NA";
  return `${ms.toFixed(ms >= 100 ? 0 : 1)} ms`;
}

// Loss is what turns a plausible latency into an unusable route, so it is the
// value the tiles are toned by.
function lossTone(lossPct: number | undefined): string {
  if (lossPct === undefined) return " gray";
  if (lossPct >= 50) return " danger";
  if (lossPct > 0) return " warn";
  return "";
}

const sampledAt = computed(() => {
  const newest = (snapshot.value?.latest ?? []).reduce((max, p) => Math.max(max, p.ts), 0);
  return newest > 0 ? formatDateTime(newest * 1000) : "";
});

function carrierOption(carrier: string): Record<string, any> {
  const targets = targetsFor(carrier);
  const { narrow, option } = buildFrame({
    width: window.innerWidth,
    unit: "hour",
    legend: targets.map((t) => t.city),
    tooltipUnit: "hour",
    tooltipValue: (p) => {
      const value = Array.isArray(p.value) ? p.value[1] : p.value;
      const n = Number(value);
      return Number.isFinite(n) ? `${n.toFixed(1)} ms` : "NA";
    },
  });
  const series = targets.map((target, i) => {
    // A fully-lost hour has no latency; feeding null leaves a gap in the line
    // instead of dropping the series to the axis.
    const data = (snapshot.value?.points ?? [])
      .filter((p) => p.target === target.id)
      .map((p) => [p.hourTs * 1000, p.avgMs] as [number, number]);
    return lineSeries(target.city, SOURCE_COLORS[i % SOURCE_COLORS.length], data, { showSymbol: !narrow });
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
    series,
  };
}

// Rebuilding on the timezone pick keeps the axis labels in step with the rest
// of the page.
const carrierOptions = computed(() => {
  void tzOffsetMinutes.value;
  return carriers.value.map((carrier) => ({ carrier, option: carrierOption(carrier) }));
});
</script>

<template>
  <section class="grid">
    <article class="card span-12 latency-head">
      <div>
        <p class="eyebrow">Probe target</p>
        <p class="metric-value small">Three carriers · Beijing, Shanghai, Guangzhou</p>
        <p class="metric-detail">
          10 requests every 5 minutes, 7 days of history.
          <span v-if="sampledAt"> Last probe {{ sampledAt }}.</span>
        </p>
      </div>
      <label class="source-picker">
        <span class="eyebrow">Node</span>
        <select v-model="selected">
          <option v-for="source in sources" :key="sourceKey(source)" :value="sourceKey(source)">{{ source.name }}</option>
        </select>
      </label>
    </article>
  </section>

  <p v-if="loadError" class="no-data">
    Latency data is unavailable for this node: {{ loadError }}. A node still running an older agent does not
    report latency until it is upgraded.
  </p>
  <p v-else-if="loading && !snapshot" class="no-data">Loading latency data...</p>
  <p v-else-if="snapshot && snapshot.targets.length === 0" class="no-data">
    This node reports no latency targets. Latency sampling needs a ping utility on the host.
  </p>

  <template v-else-if="snapshot">
    <section class="grid sources" aria-label="latest latency">
      <article v-for="target in snapshot.targets" :key="target.id" class="card metric-card span-4 latency-tile">
        <div class="metric-head">
          <div>
            <p class="eyebrow">{{ target.carrier }} · {{ target.city }}</p>
            <p class="metric-value">{{ latencyText(latestFor(target.id)?.avgMs) }}</p>
            <p class="metric-detail">{{ target.address }}</p>
          </div>
          <span :class="`status${lossTone(latestFor(target.id)?.lossPct)}`">
            <i class="dot"></i>
            {{ latestFor(target.id) ? `${Math.round(latestFor(target.id)!.lossPct)}% loss` : "No data" }}
          </span>
        </div>
      </article>
    </section>

    <section class="grid sources" aria-label="latency trends">
      <article v-for="entry in carrierOptions" :key="entry.carrier" class="card span-12">
        <h3 class="source-name">{{ entry.carrier }}</h3>
        <InlineChart :option="entry.option" />
      </article>
    </section>
  </template>
</template>

<style scoped>
.latency-head { display: flex; flex-wrap: wrap; align-items: flex-end; justify-content: space-between; gap: 16px; }
.source-picker { display: flex; flex-direction: column; gap: 7px; }
.source-picker select {
  border: 1px solid var(--line); border-radius: 12px; padding: 9px 12px;
  background: white; color: var(--text); font: inherit; font-size: 14px; font-weight: 650;
  min-width: 200px; cursor: pointer;
}
.latency-tile .metric-head { margin-bottom: 0; min-height: 0; }
</style>
