<script setup lang="ts">
import type { SourceSummary } from "../types";
import { formatBytes, formatRate, formatGMTDateTime } from "../utils";

defineProps<{ source: SourceSummary }>();
defineEmits<{ click: [] }>();

function fmtPct(v: number | undefined): string {
  if (v === undefined || v === null) return "NA";
  return `${v.toFixed(1)}%`;
}

function fmtUsage(used: number | undefined, total: number | undefined): string {
  if (!used && !total) return "";
  return `${formatBytes(used ?? 0)} / ${formatBytes(total ?? 0)}`;
}
</script>

<template>
  <article class="card source-card clickable" @click="$emit('click')">
    <div class="source-head">
      <div>
        <p class="eyebrow">Resource Source</p>
        <h2 class="source-name">{{ source.name }}</h2>
        <div class="source-meta">
          <span v-if="source.resetTime">Reset Time: {{ formatGMTDateTime(source.resetTime) }}</span>
          <span>Sample Time: {{ source.sampledAt ? formatGMTDateTime(source.sampledAt) : "NA" }}</span>
        </div>
      </div>
    </div>
    <div class="card-head">
      <span class="status"><span class="dot"></span>{{ source.resources ? "Online" : "No Data" }}</span>
      <span class="chart-hint">Click to view trend</span>
    </div>
    <div class="resource-grid" v-if="source.resources">
      <div class="resource-item">
        <div class="resource-label">CPU</div>
        <div class="resource-value" :class="{ warn: (source.resources.cpuPct ?? 0) >= 75, danger: (source.resources.cpuPct ?? 0) >= 90 }">
          {{ fmtPct(source.resources.cpuPct) }}
        </div>
        <div class="resource-detail" aria-hidden="true"></div>
        <div class="progress" :style="{ '--value': source.resources.cpuPct, '--bar': 'var(--blue)' }"></div>
      </div>
      <div class="resource-item">
        <div class="resource-label">Memory</div>
        <div class="resource-value" :class="{ warn: (source.resources.memPct ?? 0) >= 75, danger: (source.resources.memPct ?? 0) >= 90 }">
          {{ fmtPct(source.resources.memPct) }}
        </div>
        <div class="resource-detail">{{ source.resources.memTotalBytes ? fmtUsage(source.resources.memUsedBytes, source.resources.memTotalBytes) : "" }}</div>
        <div class="progress" :style="{ '--value': source.resources.memPct, '--bar': 'var(--cyan)' }"></div>
      </div>
      <div class="resource-item">
        <div class="resource-label">Disk Usage</div>
        <div class="resource-value" :class="{ warn: (source.resources.diskUsagePct ?? 0) >= 75, danger: (source.resources.diskUsagePct ?? 0) >= 90 }">
          {{ fmtPct(source.resources.diskUsagePct) }}
        </div>
        <div class="resource-detail">{{ source.resources.diskTotalBytes ? fmtUsage(source.resources.diskUsedBytes, source.resources.diskTotalBytes) : "" }}</div>
        <div class="progress" :style="{ '--value': source.resources.diskUsagePct, '--bar': 'var(--green)' }"></div>
      </div>
      <div class="resource-item">
        <div class="resource-label">IO Read</div>
        <div class="resource-value small">{{ formatRate(source.resources.diskIOReadRate) }}</div>
      </div>
      <div class="resource-item">
        <div class="resource-label">IO Write</div>
        <div class="resource-value small">{{ formatRate(source.resources.diskIOWriteRate) }}</div>
      </div>
    </div>
    <div v-else class="no-data">Resource data unavailable</div>
  </article>
</template>
