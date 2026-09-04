<script setup lang="ts">
import type { SourceSummary, UsageRow } from "../types";
import StampChip from "./StampChip.vue";
import { formatBytes, percentFor, percentText, barStyle, peakPercent } from "../utils";

defineProps<{ source: SourceSummary }>();
defineEmits<{ click: [] }>();

// Matches the r=34 circles in the ring SVG below.
const RING_C = 2 * Math.PI * 34;

function rowsForSource(source: SourceSummary): UsageRow[] {
  return [
    { label: "IN", key: "in", used: source.inUsedBytes, limit: source.inLimitBytes, color: "var(--blue)" },
    { label: "OUT", key: "out", used: source.outUsedBytes, limit: source.outLimitBytes, color: "var(--cyan)" },
    { label: "TOTAL", key: "total", used: source.totalUsedBytes, limit: source.totalLimitBytes, color: "var(--green)" },
  ];
}

function percentsForSource(source: SourceSummary) {
  return rowsForSource(source).map((row) => ({ row, percent: percentFor(row.used, row.limit) }));
}

function rowText(row: UsageRow): string {
  return row.limit > 0 ? `${formatBytes(row.used)} / ${formatBytes(row.limit)}` : formatBytes(row.used);
}

function percentClass(percent: number | null): string {
  if (percent === null) return "unlimited";
  if (percent >= 100) return "danger";
  if (percent >= 75) return "warn";
  return "";
}

// A row's bar takes the alarm colour once it is worth alarm, so a card with
// one quota nearly spent says so at a glance rather than only in its ring.
function rowColor(row: UsageRow, percent: number | null): string {
  const level = percentClass(percent);
  if (level === "danger") return "var(--red)";
  if (level === "warn") return "var(--yellow)";
  return row.color;
}

function sourceStatusClass(source: SourceSummary): string {
  const level = percentClass(peakPercent(source));
  return level === "unlimited" ? "" : level;
}

function sourceStatusLabel(source: SourceSummary): string {
  const percent = peakPercent(source);
  if (percent !== null && percent >= 100) return "Limited";
  return "Running";
}

function ringColor(percent: number | null): string {
  const level = percentClass(percent);
  if (level === "danger") return "var(--red)";
  if (level === "warn") return "var(--yellow)";
  return "var(--blue)";
}

function ringOffset(percent: number | null): number {
  const clamped = Math.min(100, Math.max(0, percent ?? 0));
  return RING_C * (1 - clamped / 100);
}
</script>

<template>
  <article class="card node-card traffic-card clickable" @click="$emit('click')">
    <div class="node-head">
      <div class="node-title">
        <span class="dot-only" :class="sourceStatusClass(source) === 'danger' ? 'danger' : sourceStatusClass(source) === 'warn' ? 'warn' : 'ok'"></span>
        <div>
          <h2 class="node-name">{{ source.name }}</h2>
          <p class="node-meta">
            <span>{{ source.id }}</span>
            <StampChip v-if="source.resetTime" kind="reset" :at="source.resetTime" />
          </p>
        </div>
      </div>
      <div class="node-side">
        <span :class="`status ${sourceStatusClass(source)}`">
          <span class="dot"></span>{{ sourceStatusLabel(source) }}
        </span>
      </div>
    </div>

    <div class="tc-body">
      <div class="gauge" title="The fullest of this node's quotas">
        <div class="ring-wrap">
          <svg class="ring" viewBox="0 0 80 80">
            <circle class="ring-bg" cx="40" cy="40" r="34" />
            <circle
              class="ring-fg"
              cx="40" cy="40" r="34"
              :style="{ stroke: ringColor(peakPercent(source)), color: ringColor(peakPercent(source)), strokeDashoffset: ringOffset(peakPercent(source)) }"
            />
          </svg>
          <span
            class="ring-value"
            :class="[percentClass(peakPercent(source)), { infinite: peakPercent(source) === null }]"
          >{{ peakPercent(source) === null ? "∞" : `${Math.round(peakPercent(source)!)}%` }}</span>
        </div>
        <div class="gauge-label">Max</div>
      </div>
      <div class="usage-rows">
        <div v-for="item in percentsForSource(source)" :key="item.row.key" class="usage-row">
          <div class="row-label">
            <strong>{{ item.row.label }}</strong>
            <span>{{ rowText(item.row) }}</span>
          </div>
          <div class="percent" :class="percentClass(item.percent)">{{ percentText(item.percent) }}</div>
          <div class="progress" :class="{ empty: item.percent === null }" :style="barStyle(item.percent, rowColor(item.row, item.percent))"></div>
        </div>
      </div>
    </div>

    <div class="node-foot">
      <StampChip kind="sampled" :at="source.sampledAt" />
      <span class="view-trend">
        View trend
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M6 3l5 5-5 5" />
        </svg>
      </span>
    </div>
  </article>
</template>
