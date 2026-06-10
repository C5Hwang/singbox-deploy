<script setup lang="ts">
import { ref, computed } from "vue";
import SourceCard from "../components/SourceCard.vue";
import TrendModal from "../components/TrendModal.vue";
import { formatBytes, percentFor, percentText, tone, barStyle } from "../utils";
import type { Summary, SourceSummary } from "../types";

const props = defineProps<{ summary: Summary | null; error: string }>();
const modalSource = ref<SourceSummary | null>(null);

const sources = computed<SourceSummary[]>(() => {
  const s = props.summary;
  if (!s) return [];
  if (s.sources && s.sources.length > 0) return s.sources;
  return [{ ...s, name: "Local Server" }];
});

type UsedKey = "inUsedBytes" | "outUsedBytes" | "totalUsedBytes";
type LimitKey = "inLimitBytes" | "outLimitBytes" | "totalLimitBytes";

interface TrafficCard {
  label: string;
  used: number;
  percent: number | null;
  detail: string;
  color: string;
}

// Unlimited sources (limit = 0) still count toward the displayed usage, but
// the quota percentage only compares limited sources against their limits.
function sumOf(usedKey: UsedKey, limitKey: LimitKey) {
  let used = 0, limitedUsed = 0, limit = 0, unlimited = 0;
  for (const src of sources.value) {
    const u = src[usedKey] ?? 0;
    const l = src[limitKey] ?? 0;
    used += u;
    if (l > 0) {
      limit += l;
      limitedUsed += u;
    } else {
      unlimited++;
    }
  }
  return { used, limitedUsed, limit, unlimited };
}

const cards = computed<TrafficCard[]>(() => {
  const defs: { label: string; usedKey: UsedKey; limitKey: LimitKey; color: string }[] = [
    { label: "Inbound", usedKey: "inUsedBytes", limitKey: "inLimitBytes", color: "var(--blue)" },
    { label: "Outbound", usedKey: "outUsedBytes", limitKey: "outLimitBytes", color: "var(--cyan)" },
    { label: "Total", usedKey: "totalUsedBytes", limitKey: "totalLimitBytes", color: "var(--green)" },
  ];
  return defs.map((d) => {
    const { used, limitedUsed, limit, unlimited } = sumOf(d.usedKey, d.limitKey);
    const percent = limit > 0 ? percentFor(limitedUsed, limit) : null;
    let detail: string;
    if (limit <= 0) {
      detail = sources.value.length > 0 ? "No quota configured" : "";
    } else {
      detail = `Quota ${formatBytes(limitedUsed)} / ${formatBytes(limit)}`;
      if (unlimited > 0) detail += ` · ${unlimited} unlimited`;
    }
    return { label: d.label, used, percent, detail, color: d.color };
  });
});

const availableCount = computed(() => {
  const srcs = sources.value;
  const total = srcs.length;
  if (total === 0) return { running: 0, total: 0, percent: null as number | null, unavailablePercent: null as number | null };
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

const availableDetail = computed(() => {
  const { running, total } = availableCount.value;
  if (total === 0) return "";
  const limited = total - running;
  return limited > 0 ? `${limited} source${limited > 1 ? "s" : ""} limited` : "All sources running";
});
</script>

<template>
  <section class="grid">
    <article class="card metric-card span-3">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Available</p>
          <p class="metric-value">{{ availableCount.running }} / {{ availableCount.total }}</p>
          <p class="metric-detail">{{ availableDetail }}</p>
        </div>
        <span :class="`delta${tone(availableCount.unavailablePercent)}`">{{ percentText(availableCount.percent) }}</span>
      </div>
      <div class="progress" :style="barStyle(availableCount.percent, 'var(--green)')"></div>
    </article>

    <article v-for="card in cards" :key="card.label" class="card metric-card span-3">
      <div class="metric-head">
        <div>
          <p class="eyebrow">{{ card.label }}</p>
          <p class="metric-value">{{ formatBytes(card.used) }}</p>
          <p class="metric-detail">{{ card.detail }}</p>
        </div>
        <span :class="`delta${tone(card.percent)}`">{{ percentText(card.percent) }}</span>
      </div>
      <div class="progress" :class="{ empty: card.percent === null }" :style="barStyle(card.percent, card.color)"></div>
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
