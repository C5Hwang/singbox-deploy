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

// Which node is highest on each reading, so the tile can name it.
interface Peak {
  res: ResourceSnapshot;
  cpuName: string;
  memName: string;
  diskName: string;
}

const peakRes = computed<Peak | undefined>(() => {
  const all = sources.value.filter((s) => s.resources) as (SourceSummary & { resources: ResourceSnapshot })[];
  if (all.length === 0) return undefined;
  let bestCpu = all[0], bestMem = all[0], bestDisk = all[0];
  for (const s of all) {
    if (s.resources.cpuPct > bestCpu.resources.cpuPct) bestCpu = s;
    if (s.resources.memPct > bestMem.resources.memPct) bestMem = s;
    if (s.resources.diskUsagePct > bestDisk.resources.diskUsagePct) bestDisk = s;
  }
  return {
    res: {
      cpuPct: bestCpu.resources.cpuPct,
      memPct: bestMem.resources.memPct,
      memUsedBytes: bestMem.resources.memUsedBytes,
      memTotalBytes: bestMem.resources.memTotalBytes,
      diskUsagePct: bestDisk.resources.diskUsagePct,
      diskUsedBytes: bestDisk.resources.diskUsedBytes,
      diskTotalBytes: bestDisk.resources.diskTotalBytes,
      diskIOReadRate: 0,
      diskIOWriteRate: 0,
    },
    cpuName: all.length > 1 ? bestCpu.name : "",
    memName: all.length > 1 ? bestMem.name : "",
    diskName: all.length > 1 ? bestDisk.name : "",
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

// The tiles report the fleet's highest reading of each kind, and say which
// node it is on: a fleet-wide "91% CPU" is only useful with a name attached.
const metricCards = computed<ResourceCardDef[]>(() => {
  const p = peakRes.value;
  const r = p?.res;
  const on = (name: string, fallback: string) => (name ? `Highest · ${name}` : fallback);
  return [
    { key: "cpu", label: "Peak CPU", pct: r?.cpuPct ?? null, detail: p ? on(p.cpuName, "") : "", color: "var(--blue)" },
    { key: "mem", label: "Peak Memory", pct: r?.memPct ?? null, detail: p ? on(p.memName, fmtUsage(r!.memUsedBytes, r!.memTotalBytes)) : "", color: "var(--cyan)" },
    { key: "disk", label: "Peak Disk", pct: r?.diskUsagePct ?? null, detail: p ? on(p.diskName, fmtUsage(r!.diskUsedBytes, r!.diskTotalBytes)) : "", color: "var(--green)" },
  ];
});
</script>

<template>
  <section class="tiles tiles-3" aria-label="fleet peaks">
    <article
      v-for="card in metricCards"
      :key="card.key"
      class="card tile clickable"
      :title="`Open the ${card.label.toLowerCase()} trend across every node`"
      @click="modalMetric = { kind: 'resource', title: card.label.replace('Peak ', ''), key: card.key }"
    >
      <div class="metric-head">
        <div>
          <p class="eyebrow">{{ card.label }}</p>
          <p class="metric-value">{{ fmtPct(card.pct) }}</p>
          <p class="metric-detail">{{ card.detail }}</p>
        </div>
        <div class="metric-side">
          <span :class="`delta${tone(card.pct)}`">Live</span>
          <span class="tile-go" aria-hidden="true">
            <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M6 3l5 5-5 5" />
            </svg>
          </span>
        </div>
      </div>
      <div class="progress" :style="barStyle(card.pct, card.color)"></div>
    </article>
  </section>

  <section class="nodes sources" aria-label="resource sources">
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
