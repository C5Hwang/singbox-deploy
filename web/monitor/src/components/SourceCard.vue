<script setup lang="ts">
import type { SourceSummary, UsageRow } from "../types";
import { formatBytes, percentFor, percentText, barStyle, formatGMTDateTime } from "../utils";

defineProps<{ source: SourceSummary }>();
defineEmits<{ click: [] }>();

// Matches the r=34 circles in the ring SVG below.
const RING_C = 2 * Math.PI * 34;

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
  const configured = percentsForSource(source)
    .map((item) => item.percent)
    .filter((p): p is number => p !== null);
  if (configured.length === 0) return null;
  return Math.max(...configured);
}

function rowText(row: UsageRow): string {
  return row.limit > 0 ? `${formatBytes(row.used)} / ${formatBytes(row.limit)}` : formatBytes(row.used);
}

function percentClass(percent: number | null): string {
  if (percent !== null && percent >= 100) return "danger";
  if (percent !== null && percent >= 75) return "warn";
  return "";
}

function sourceStatusClass(source: SourceSummary): string {
  return percentClass(peakForSource(source));
}

function sourceStatusLabel(source: SourceSummary): string {
  const percent = peakForSource(source);
  if (percent !== null && percent >= 100) return "Limited";
  return "Running";
}

function ringColor(percent: number | null): string {
  const level = percentClass(percent);
  if (level === "danger") return "var(--red)";
  if (level === "warn") return "var(--orange)";
  return "var(--blue)";
}

function ringOffset(percent: number | null): number {
  const clamped = Math.min(100, Math.max(0, percent ?? 0));
  return RING_C * (1 - clamped / 100);
}
</script>

<template>
  <article class="card source-card traffic-card clickable" @click="$emit('click')">
    <div class="rc-head">
      <div class="rc-title">
        <p class="eyebrow">Monitor Source</p>
        <h2 class="source-name">{{ source.name }}</h2>
      </div>
      <div class="rc-side">
        <span :class="`status ${sourceStatusClass(source)}`">
          <span class="dot"></span>{{ sourceStatusLabel(source) }}
        </span>
        <span class="view-trend">
          View Trend
          <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M6 3l5 5-5 5" />
          </svg>
        </span>
      </div>
    </div>

    <div class="tc-body">
      <div class="gauge">
        <div class="ring-wrap">
          <svg class="ring" viewBox="0 0 80 80">
            <circle class="ring-bg" cx="40" cy="40" r="34" />
            <circle
              class="ring-fg"
              cx="40" cy="40" r="34"
              :style="{ stroke: ringColor(peakForSource(source)), strokeDashoffset: ringOffset(peakForSource(source)) }"
            />
          </svg>
          <span
            class="ring-value"
            :class="[percentClass(peakForSource(source)), { infinite: peakForSource(source) === null }]"
          >{{ peakForSource(source) === null ? "∞" : `${Math.round(peakForSource(source)!)}%` }}</span>
        </div>
        <div class="gauge-label">Max Usage</div>
      </div>
      <div class="usage-rows">
        <div v-for="item in percentsForSource(source)" :key="item.row.key" class="usage-row">
          <div class="row-label">
            <strong>{{ item.row.label }}</strong>
            <span>{{ rowText(item.row) }}</span>
          </div>
          <div class="progress" :class="{ empty: item.percent === null }" :style="barStyle(item.percent, item.row.color)"></div>
          <div class="percent" :class="percentClass(item.percent)">{{ percentText(item.percent) }}</div>
        </div>
      </div>
    </div>

    <div class="rc-meta">
      <span>Reset: {{ source.resetTime ? formatGMTDateTime(source.resetTime) : "NA" }}</span>
      <span>Sampled: {{ source.sampledAt ? formatGMTDateTime(source.sampledAt) : "NA" }}</span>
    </div>
  </article>
</template>
