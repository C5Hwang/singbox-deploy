<script setup lang="ts">
import { computed } from "vue";
import type { SourceSummary } from "../types";
import { formatBytes, formatRate, formatDateTime } from "../utils";
import { formatAgo } from "../clock";

const props = defineProps<{ source: SourceSummary }>();
defineEmits<{ click: [] }>();

// Matches the r=34 circles in the ring SVG below.
const RING_C = 2 * Math.PI * 34;

interface Gauge {
  key: string;
  label: string;
  pct: number | null;
  detail: string;
  base: string;
}

function usage(used: number | undefined, total: number | undefined): string {
  if (!used && !total) return "";
  return `${formatBytes(used ?? 0)} / ${formatBytes(total ?? 0)}`;
}

const gauges = computed<Gauge[]>(() => {
  const r = props.source.resources;
  if (!r) return [];
  return [
    { key: "cpu", label: "CPU", pct: r.cpuPct ?? null, detail: "", base: "var(--blue)" },
    { key: "mem", label: "Memory", pct: r.memPct ?? null, detail: usage(r.memUsedBytes, r.memTotalBytes), base: "var(--cyan)" },
    { key: "disk", label: "Disk", pct: r.diskUsagePct ?? null, detail: usage(r.diskUsedBytes, r.diskTotalBytes), base: "var(--green)" },
  ];
});

// The machine in one line: what it has, so the percentages above it have a
// scale. It is the line a probe list prints under every node's name.
const spec = computed(() => {
  const r = props.source.resources;
  if (!r) return "";
  const parts: string[] = [];
  if (r.memTotalBytes) parts.push(`${formatBytes(r.memTotalBytes)} RAM`);
  if (r.diskTotalBytes) parts.push(`${formatBytes(r.diskTotalBytes)} disk`);
  return parts.join(" · ");
});

function levelClass(pct: number | null): string {
  if (pct !== null && pct >= 90) return "danger";
  if (pct !== null && pct >= 75) return "warn";
  return "";
}

function ringColor(g: Gauge): string {
  const level = levelClass(g.pct);
  if (level === "danger") return "var(--red)";
  if (level === "warn") return "var(--yellow)";
  return g.base;
}

function ringOffset(pct: number | null): number {
  const clamped = Math.min(100, Math.max(0, pct ?? 0));
  return RING_C * (1 - clamped / 100);
}

function pctText(pct: number | null): string {
  return pct === null ? "NA" : `${pct.toFixed(1)}%`;
}

// The dot is the worst of the three readings.
const tone = computed(() => {
  if (!props.source.resources) return "gray";
  const levels = gauges.value.map((g) => levelClass(g.pct));
  if (levels.includes("danger")) return "danger";
  if (levels.includes("warn")) return "warn";
  return "ok";
});
</script>

<template>
  <article class="card node-card resource-card clickable" @click="$emit('click')">
    <div class="node-head">
      <div class="node-title">
        <span class="dot-only" :class="tone"></span>
        <div>
          <h2 class="node-name">{{ source.name }}</h2>
          <p class="node-meta">
            <span>{{ source.id }}</span>
            <span v-if="spec">{{ spec }}</span>
          </p>
        </div>
      </div>
      <div class="node-side">
        <span class="status" :class="{ gray: !source.resources }">
          <span class="dot"></span>{{ source.resources ? "Online" : "No Data" }}
        </span>
      </div>
    </div>

    <div class="rc-body" v-if="source.resources">
      <div class="gauges">
        <div class="gauge" v-for="g in gauges" :key="g.key">
          <div class="ring-wrap">
            <svg class="ring" viewBox="0 0 80 80">
              <circle class="ring-bg" cx="40" cy="40" r="34" />
              <circle
                class="ring-fg"
                cx="40" cy="40" r="34"
                :style="{ stroke: ringColor(g), color: ringColor(g), strokeDashoffset: ringOffset(g.pct) }"
              />
            </svg>
            <span class="ring-value" :class="levelClass(g.pct)">{{ pctText(g.pct) }}</span>
          </div>
          <div class="gauge-label">{{ g.label }}</div>
          <div class="gauge-detail">{{ g.detail }}</div>
        </div>
      </div>
      <div class="io-block">
        <div class="io-stat">
          <span class="io-icon read">&darr;</span>
          <div>
            <span class="io-label">IO Read</span>
            <strong>{{ formatRate(source.resources.diskIOReadRate) }}</strong>
          </div>
        </div>
        <div class="io-stat">
          <span class="io-icon write">&uarr;</span>
          <div>
            <span class="io-label">IO Write</span>
            <strong>{{ formatRate(source.resources.diskIOWriteRate) }}</strong>
          </div>
        </div>
      </div>
    </div>
    <p v-else class="no-data">This node has not reported resource readings.</p>

    <div class="node-foot">
      <span :title="source.sampledAt ? `Sampled ${formatDateTime(source.sampledAt)}` : ''">
        sampled {{ source.sampledAt ? formatAgo(source.sampledAt) : "NA" }}
      </span>
      <span class="view-trend">
        View trend
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M6 3l5 5-5 5" />
        </svg>
      </span>
    </div>
  </article>
</template>
