<script setup lang="ts">
import { ref, computed } from "vue";
import ResourceSourceCard from "../components/ResourceSourceCard.vue";
import ResourceModal from "../components/ResourceModal.vue";
import MetricTrendModal from "../components/MetricTrendModal.vue";
import { formatBytes, tone, barStyle } from "../utils";
import type { Summary, SourceSummary, ResourceSnapshot, MetricDef } from "../types";

const props = defineProps<{ summary: Summary | null; error: string }>();
const modalSource = ref<SourceSummary | null>(null);
const modalMetric = ref<MetricDef | null>(null);

const sources = computed<SourceSummary[]>(() => {
  const s = props.summary;
  if (!s) return [];
  if (s.sources && s.sources.length > 0) return s.sources;
  return [{ ...s, id: "local", name: "Local Server" }];
});

const peakRes = computed<ResourceSnapshot | undefined>(() => {
  const all = sources.value.map((s) => s.resources).filter(Boolean) as ResourceSnapshot[];
  if (all.length === 0) return undefined;
  if (all.length === 1) return all[0];
  let bestCpu = all[0], bestMem = all[0], bestDisk = all[0];
  for (const r of all) {
    if (r.cpuPct > bestCpu.cpuPct) bestCpu = r;
    if (r.memPct > bestMem.memPct) bestMem = r;
    if (r.diskUsagePct > bestDisk.diskUsagePct) bestDisk = r;
  }
  return {
    cpuPct: bestCpu.cpuPct,
    memPct: bestMem.memPct,
    memUsedBytes: bestMem.memUsedBytes,
    memTotalBytes: bestMem.memTotalBytes,
    diskUsagePct: bestDisk.diskUsagePct,
    diskUsedBytes: bestDisk.diskUsedBytes,
    diskTotalBytes: bestDisk.diskTotalBytes,
    diskIOReadRate: 0,
    diskIOWriteRate: 0,
  };
});

function fmtPct(v: number | undefined | null): string {
  if (v === undefined || v === null) return "NA";
  return `${v.toFixed(1)}%`;
}

function fmtUsage(used: number | undefined, total: number | undefined): string {
  if (!used && !total) return "";
  return `${formatBytes(used ?? 0)} / ${formatBytes(total ?? 0)}`;
}

interface ResourceCardDef {
  key: "cpu" | "mem" | "disk";
  label: string;
  pct: number | null;
  detail: string;
  color: string;
}

const metricCards = computed<ResourceCardDef[]>(() => {
  const r = peakRes.value;
  return [
    { key: "cpu", label: "CPU", pct: r?.cpuPct ?? null, detail: "", color: "var(--blue)" },
    { key: "mem", label: "Memory", pct: r?.memPct ?? null, detail: r ? fmtUsage(r.memUsedBytes, r.memTotalBytes) : "", color: "var(--cyan)" },
    { key: "disk", label: "Disk Usage", pct: r?.diskUsagePct ?? null, detail: r ? fmtUsage(r.diskUsedBytes, r.diskTotalBytes) : "", color: "var(--green)" },
  ];
});
</script>

<template>
  <section class="grid">
    <article
      v-for="card in metricCards"
      :key="card.key"
      class="card metric-card span-4 clickable"
      @click="modalMetric = { kind: 'resource', title: card.label, key: card.key }"
    >
      <div class="metric-head">
        <div>
          <p class="eyebrow">{{ card.label }}</p>
          <p class="metric-value">{{ fmtPct(card.pct) }}</p>
          <p class="metric-detail">{{ card.detail }}</p>
        </div>
        <div class="metric-side">
          <span :class="`delta${tone(card.pct)}`">Live</span>
          <span class="view-trend">
            View Trend
            <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <path d="M6 3l5 5-5 5" />
            </svg>
          </span>
        </div>
      </div>
      <div class="progress" :style="barStyle(card.pct, card.color)"></div>
    </article>
  </section>

  <section class="grid sources" aria-label="resource sources">
    <ResourceSourceCard
      v-for="source in sources"
      :key="source.id || source.name"
      :source="source"
      @click="modalSource = source"
    />
  </section>

  <ResourceModal
    v-if="modalSource"
    :source="modalSource"
    @close="modalSource = null"
  />

  <MetricTrendModal
    v-if="modalMetric"
    :metric="modalMetric"
    :sources="sources"
    @close="modalMetric = null"
  />
</template>
