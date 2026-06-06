<script setup lang="ts">
import { ref, computed } from "vue";
import SourceCard from "../components/SourceCard.vue";
import TrendModal from "../components/TrendModal.vue";
import { formatBytes, percentFor, percentText, tone, barStyle } from "../utils";
import type { Summary, SourceSummary, UsageRow } from "../types";

const props = defineProps<{ summary: Summary | null; error: string }>();
const modalSource = ref<SourceSummary | null>(null);

const sources = computed<SourceSummary[]>(() => {
  const s = props.summary;
  if (!s) return [];
  if (s.sources && s.sources.length > 0) return s.sources;
  return [{ ...s, name: "Local Server" }];
});

const rows = computed<UsageRow[]>(() => {
  const s = props.summary;
  return [
    { label: "IN", key: "in", used: s?.inUsedBytes ?? 0, limit: s?.inLimitBytes ?? 0, color: "var(--blue)" },
    { label: "OUT", key: "out", used: s?.outUsedBytes ?? 0, limit: s?.outLimitBytes ?? 0, color: "var(--cyan)" },
    { label: "Total", key: "total", used: s?.totalUsedBytes ?? 0, limit: s?.totalLimitBytes ?? 0, color: "var(--green)" },
  ];
});

const rowPercents = computed(() =>
  rows.value.map((row) => ({ row, percent: percentFor(row.used, row.limit) })),
);

const peak = computed(() => {
  const configured = rowPercents.value.filter((item) => item.percent !== null) as Array<{
    row: UsageRow;
    percent: number;
  }>;
  if (configured.length === 0) return null;
  return configured.reduce((best, item) => (item.percent > best.percent ? item : best), configured[0]);
});

const sourcePercent = computed(() => peak.value?.percent ?? null);
</script>

<template>
  <section class="grid">
    <article class="card metric-card span-3">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Peak Usage</p>
          <p class="metric-value">{{ percentText(sourcePercent) }}</p>
        </div>
        <span :class="`delta${tone(sourcePercent)}`">Live</span>
      </div>
      <div class="progress" :style="barStyle(sourcePercent, 'var(--blue)')"></div>
    </article>

    <article class="card metric-card span-3">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Inbound</p>
          <p class="metric-value">{{ formatBytes(summary?.inUsedBytes) }}</p>
        </div>
        <span :class="`delta${tone(rowPercents[0]?.percent ?? null)}`">{{ percentText(rowPercents[0]?.percent ?? null) }}</span>
      </div>
      <div class="progress" :style="barStyle(rowPercents[0]?.percent ?? null, 'var(--blue)')"></div>
    </article>

    <article class="card metric-card span-3">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Outbound</p>
          <p class="metric-value">{{ formatBytes(summary?.outUsedBytes) }}</p>
        </div>
        <span :class="`delta${tone(rowPercents[1]?.percent ?? null)}`">{{ percentText(rowPercents[1]?.percent ?? null) }}</span>
      </div>
      <div class="progress" :style="barStyle(rowPercents[1]?.percent ?? null, 'var(--cyan)')"></div>
    </article>

    <article class="card metric-card span-3">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Total</p>
          <p class="metric-value">{{ formatBytes(summary?.totalUsedBytes) }}</p>
        </div>
        <span :class="`delta${tone(rowPercents[2]?.percent ?? null)}`">{{ percentText(rowPercents[2]?.percent ?? null) }}</span>
      </div>
      <div class="progress" :style="barStyle(rowPercents[2]?.percent ?? null, 'var(--green)')"></div>
    </article>
  </section>

  <section class="grid sources" aria-label="monitor sources">
    <SourceCard
      v-for="source in sources"
      :key="source.name"
      :source="source"
      @click="modalSource = source"
    />
  </section>

  <TrendModal
    v-if="modalSource"
    :source="modalSource"
    @close="modalSource = null"
  />
</template>
