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
  if (!s) {
    return [
      { label: "IN", key: "in", used: 0, limit: 0, color: "var(--blue)" },
      { label: "OUT", key: "out", used: 0, limit: 0, color: "var(--cyan)" },
      { label: "Total", key: "total", used: 0, limit: 0, color: "var(--green)" },
    ];
  }

  const srcs = s.sources && s.sources.length > 0 ? s.sources : null;

  function sumOf(
    usedKey: "inUsedBytes" | "outUsedBytes" | "totalUsedBytes",
    limitKey: "inLimitBytes" | "outLimitBytes" | "totalLimitBytes",
  ): { used: number; limit: number } {
    if (!srcs) return { used: s![usedKey] ?? 0, limit: s![limitKey] ?? 0 };
    let totalUsed = 0, totalLimit = 0;
    for (const src of srcs) {
      totalUsed += src[usedKey] ?? 0;
      totalLimit += src[limitKey] ?? 0;
    }
    return { used: totalUsed, limit: totalLimit };
  }

  const inSum = sumOf("inUsedBytes", "inLimitBytes");
  const outSum = sumOf("outUsedBytes", "outLimitBytes");
  const totalSum = sumOf("totalUsedBytes", "totalLimitBytes");

  return [
    { label: "IN", key: "in", used: inSum.used, limit: inSum.limit, color: "var(--blue)" },
    { label: "OUT", key: "out", used: outSum.used, limit: outSum.limit, color: "var(--cyan)" },
    { label: "Total", key: "total", used: totalSum.used, limit: totalSum.limit, color: "var(--green)" },
  ];
});

const rowPercents = computed(() =>
  rows.value.map((row) => ({ row, percent: percentFor(row.used, row.limit) })),
);

const availableCount = computed(() => {
  const srcs = sources.value;
  const total = srcs.length;
  if (total === 0) return { running: 0, total: 0, percent: null as number | null };
  let running = 0;
  for (const src of srcs) {
    const rows = [
      percentFor(src.inUsedBytes, src.inLimitBytes),
      percentFor(src.outUsedBytes, src.outLimitBytes),
      percentFor(src.totalUsedBytes, src.totalLimitBytes),
    ];
    const configured = rows.filter((p) => p !== null) as number[];
    const peak = configured.length > 0 ? Math.max(...configured) : 0;
    if (peak < 100) running++;
  }
  const percent = (running / total) * 100;
  const unavailablePercent = 100 - percent;
  return { running, total, percent, unavailablePercent };
});
</script>

<template>
  <section class="grid">
    <article class="card metric-card span-3">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Available</p>
          <p class="metric-value">{{ availableCount.running }} / {{ availableCount.total }}</p>
        </div>
        <span :class="`delta${tone(availableCount.unavailablePercent)}`">{{ percentText(availableCount.percent) }}</span>
      </div>
      <div class="progress" :style="barStyle(availableCount.percent, 'var(--green)')"></div>
    </article>

    <article class="card metric-card span-3">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Inbound</p>
          <p class="metric-value">{{ formatBytes(rows[0]?.used) }}</p>
        </div>
        <span :class="`delta${tone(rowPercents[0]?.percent ?? null)}`">{{ percentText(rowPercents[0]?.percent ?? null) }}</span>
      </div>
      <div class="progress" :style="barStyle(rowPercents[0]?.percent ?? null, 'var(--blue)')"></div>
    </article>

    <article class="card metric-card span-3">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Outbound</p>
          <p class="metric-value">{{ formatBytes(rows[1]?.used) }}</p>
        </div>
        <span :class="`delta${tone(rowPercents[1]?.percent ?? null)}`">{{ percentText(rowPercents[1]?.percent ?? null) }}</span>
      </div>
      <div class="progress" :style="barStyle(rowPercents[1]?.percent ?? null, 'var(--cyan)')"></div>
    </article>

    <article class="card metric-card span-3">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Total</p>
          <p class="metric-value">{{ formatBytes(rows[2]?.used) }}</p>
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
