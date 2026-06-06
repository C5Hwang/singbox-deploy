<script setup lang="ts">
import type { SourceSummary, UsageRow } from "../types";
import { formatBytes, percentFor, percentText, tone, barStyle, formatGMTDateTime } from "../utils";

const props = defineProps<{ source: SourceSummary }>();
defineEmits<{ click: [] }>();

function rowsForSource(source: SourceSummary): UsageRow[] {
  return [
    { label: "IN", key: "in", used: source.inUsedBytes, limit: source.inLimitBytes, color: "var(--blue)" },
    { label: "OUT", key: "out", used: source.outUsedBytes, limit: source.outLimitBytes, color: "var(--cyan)" },
    { label: "Total", key: "total", used: source.totalUsedBytes, limit: source.totalLimitBytes, color: "var(--green)" },
  ];
}

function percentsForSource(source: SourceSummary) {
  return rowsForSource(source).map((row) => ({ row, percent: percentFor(row.used, row.limit) }));
}

function peakForSource(source: SourceSummary): number | null {
  const configured = percentsForSource(source).filter((item) => item.percent !== null) as Array<{
    row: UsageRow;
    percent: number;
  }>;
  if (configured.length === 0) return null;
  return configured.reduce((best, item) => (item.percent > best.percent ? item : best), configured[0]).percent;
}

function rowText(row: UsageRow): string {
  return row.limit > 0 ? `${formatBytes(row.used)} / ${formatBytes(row.limit)}` : formatBytes(row.used);
}

function sourceStatusClass(source: SourceSummary): string {
  const percent = peakForSource(source);
  if (percent !== null && percent >= 100) return " danger";
  if (percent !== null && percent >= 75) return " warn";
  return "";
}

function sourceStatusLabel(source: SourceSummary): string {
  const percent = peakForSource(source);
  if (percent !== null && percent >= 100) return "Limited";
  return "Running";
}

function sourceResetLabel(source: SourceSummary): string {
  return source.resetTime ? formatGMTDateTime(source.resetTime) : "NA";
}
</script>

<template>
  <article class="card source-card clickable" @click="$emit('click')">
    <div class="source-head">
      <div>
        <p class="eyebrow">Monitor Source</p>
        <h2 class="source-name">{{ source.name }}</h2>
        <div class="source-meta">
          <span>Reset Time: {{ sourceResetLabel(source) }}</span>
          <span>Sample Time: {{ source.sampledAt ? formatGMTDateTime(source.sampledAt) : "NA" }}</span>
          <span v-if="source.fetchedAt">Fetched: {{ formatGMTDateTime(source.fetchedAt) }}</span>
        </div>
      </div>
      <div>
        <div class="source-score">{{ percentText(peakForSource(source)) }}<span>MAX USAGE</span></div>
      </div>
    </div>
    <div class="card-head">
      <span :class="`status${sourceStatusClass(source)}`"><span class="dot"></span>{{ sourceStatusLabel(source) }}</span>
      <span v-if="peakForSource(source) !== null && (peakForSource(source) ?? 0) >= 100" class="tag red">Quota</span>
      <span class="chart-hint">Click to view trend</span>
    </div>
    <div class="source-rows">
      <div v-for="item in percentsForSource(source)" :key="item.row.key" class="progress-row">
        <div class="row-label">
          <strong>{{ item.row.label }}</strong>
          <span>{{ rowText(item.row) }}</span>
        </div>
        <div class="progress" :class="{ empty: item.percent === null }" :style="barStyle(item.percent, item.row.color)"></div>
        <div class="percent">{{ percentText(item.percent) }}</div>
      </div>
    </div>
  </article>
</template>
