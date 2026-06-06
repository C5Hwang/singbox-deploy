<script setup lang="ts">
import { ref, computed } from "vue";
import ResourceSourceCard from "../components/ResourceSourceCard.vue";
import ResourceModal from "../components/ResourceModal.vue";
import { formatBytes, tone, barStyle } from "../utils";
import type { Summary, SourceSummary, ResourceSnapshot } from "../types";

const props = defineProps<{ summary: Summary | null; error: string }>();
const modalSource = ref<SourceSummary | null>(null);

const sources = computed<SourceSummary[]>(() => {
  const s = props.summary;
  if (!s) return [];
  if (s.sources && s.sources.length > 0) return s.sources;
  return [{ ...s, name: "Local Server" }];
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
</script>

<template>
  <section class="grid">
    <article class="card metric-card span-4">
      <div class="metric-head">
        <div>
          <p class="eyebrow">CPU</p>
          <p class="metric-value">{{ fmtPct(peakRes?.cpuPct) }}</p>
        </div>
        <span :class="`delta${tone(peakRes?.cpuPct ?? null)}`">Live</span>
      </div>
      <div class="progress" :style="barStyle(peakRes?.cpuPct ?? null, 'var(--blue)')"></div>
    </article>

    <article class="card metric-card span-4">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Memory</p>
          <p class="metric-value">{{ fmtPct(peakRes?.memPct) }}</p>
          <p class="metric-detail" v-if="peakRes">{{ fmtUsage(peakRes.memUsedBytes, peakRes.memTotalBytes) }}</p>
        </div>
        <span :class="`delta${tone(peakRes?.memPct ?? null)}`">Live</span>
      </div>
      <div class="progress" :style="barStyle(peakRes?.memPct ?? null, 'var(--cyan)')"></div>
    </article>

    <article class="card metric-card span-4">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Disk Usage</p>
          <p class="metric-value">{{ fmtPct(peakRes?.diskUsagePct) }}</p>
          <p class="metric-detail" v-if="peakRes">{{ fmtUsage(peakRes.diskUsedBytes, peakRes.diskTotalBytes) }}</p>
        </div>
        <span :class="`delta${tone(peakRes?.diskUsagePct ?? null)}`">Live</span>
      </div>
      <div class="progress" :style="barStyle(peakRes?.diskUsagePct ?? null, 'var(--green)')"></div>
    </article>
  </section>

  <section class="grid sources" aria-label="resource sources">
    <ResourceSourceCard
      v-for="source in sources"
      :key="source.name"
      :source="source"
      @click="modalSource = source"
    />
  </section>

  <ResourceModal
    v-if="modalSource"
    :source="modalSource"
    @close="modalSource = null"
  />
</template>
