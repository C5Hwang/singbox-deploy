<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";

interface HourlyPoint {
  hourTs: number;
  inBytes: number;
  outBytes: number;
  totalBytes: number;
}

interface Summary {
  inUsedBytes: number;
  outUsedBytes: number;
  totalUsedBytes: number;
  inRemainingBytes: number;
  outRemainingBytes: number;
  totalRemainingBytes: number;
  inLimitBytes: number;
  outLimitBytes: number;
  totalLimitBytes: number;
  resetTime: string;
  trend: HourlyPoint[];
}

interface UsageRow {
  label: string;
  key: "in" | "out" | "total";
  used: number;
  limit: number;
  color: string;
}

const summary = ref<Summary | null>(null);
const error = ref<string>("");
const now = ref(new Date());
let loadTimer: number | undefined;
let clockTimer: number | undefined;

// The UI is served under /traffic/, so the API is reached via a relative path
// that resolves to /traffic/api/summary.
async function load() {
  try {
    const res = await fetch("api/summary", { cache: "no-store" });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    summary.value = await res.json();
    error.value = "";
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

function formatBytes(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(Number(value))) return "NA";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let size = Math.max(0, Number(value));
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index += 1;
  }
  const digits = index === 0 ? 0 : size >= 10 ? 1 : 2;
  return `${size.toFixed(digits)} ${units[index]}`;
}

function percentFor(used: number, limit: number): number | null {
  if (limit <= 0) return null;
  return Math.min(100, Math.max(0, (used / limit) * 100));
}

function percentText(value: number | null): string {
  if (value === null) return "Unlimited";
  return `${Math.round(value)}%`;
}

function rowText(row: UsageRow): string {
  return row.limit > 0 ? `${formatBytes(row.used)} / ${formatBytes(row.limit)}` : formatBytes(row.used);
}

function tone(percent: number | null): string {
  if (percent !== null && percent >= 90) return " danger";
  if (percent !== null && percent >= 75) return " warn";
  return "";
}

function barStyle(percent: number | null, color: string): Record<string, string> {
  return {
    "--value": String(percent === null ? 0 : percent),
    "--bar": color,
  };
}

const clockLabel = computed(() =>
  now.value.toLocaleTimeString("en-US", { hour12: false }),
);

const resetLabel = computed(() =>
  summary.value ? new Date(summary.value.resetTime).toLocaleString("en-US") : "NA",
);

const rows = computed<UsageRow[]>(() => {
  const s = summary.value;
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
const isLimited = computed(() => rowPercents.value.some((item) => item.percent !== null && item.percent >= 100));
const statusClass = computed(() => {
  if (error.value) return " gray";
  if (isLimited.value) return " danger";
  if (sourcePercent.value !== null && sourcePercent.value >= 75) return " warn";
  return "";
});
const statusLabel = computed(() => {
  if (error.value) return "Unavailable";
  if (isLimited.value) return "Limited";
  return "Running";
});

const sourceChip = computed(() => (summary.value ? "Source 1/1" : "Source 0/1"));
const subtitle = computed(() => {
  if (error.value) return `Failed to load data: ${error.value}`;
  if (!summary.value) return "Loading local traffic data";
  if (!peak.value) return "No traffic limits are configured";
  return `Highest usage direction: ${peak.value.row.label}`;
});
const sideSummary = computed(() => {
  const s = summary.value;
  return `IN ${formatBytes(s?.inUsedBytes)} | OUT ${formatBytes(s?.outUsedBytes)} | Total ${formatBytes(s?.totalUsedBytes)}`;
});
const trendCount = computed(() => summary.value?.trend.length ?? 0);

onMounted(() => {
  load();
  loadTimer = window.setInterval(load, 30000);
  clockTimer = window.setInterval(() => {
    now.value = new Date();
  }, 1000);
});
onUnmounted(() => {
  if (loadTimer) window.clearInterval(loadTimer);
  if (clockTimer) window.clearInterval(clockTimer);
});
</script>

<template>
  <div class="app">
    <aside class="sidebar" aria-label="Sidebar navigation">
      <div class="brand">
        <div class="brand-logo">TM</div>
        <div>
          <strong>Traffic Monitor</strong>
        </div>
      </div>

      <a class="nav-item active" href="#">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <path d="M3 13h8V3H3v10Zm10 8h8V3h-8v18ZM3 21h8v-6H3v6Z" />
        </svg>
        Overview
      </a>

      <div class="mini-card">
        <strong>{{ statusLabel }}</strong>
        <div class="small">{{ sideSummary }}</div>
      </div>
    </aside>

    <main class="main">
      <header class="topbar">
        <div class="title">
          <h1>Traffic Monitoring Center</h1>
          <p>{{ subtitle }}</p>
        </div>
        <div class="toolbar">
          <div class="chip">{{ sourceChip }}</div>
          <div class="chip">{{ clockLabel }}</div>
          <button class="button" type="button" @click="load">Refresh Data</button>
        </div>
      </header>

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

      <section class="grid sources" aria-label="traffic sources">
        <article class="card source-card">
          <div class="source-head">
            <div>
              <p class="eyebrow">Traffic Source</p>
              <h2 class="source-name">Local Server</h2>
              <div class="source-meta">
                <span>Reset Time: {{ resetLabel }}</span>
                <span>Trend Points: {{ trendCount }}</span>
              </div>
            </div>
            <div>
              <div class="source-score">{{ percentText(sourcePercent) }}<span>MAX USAGE</span></div>
            </div>
          </div>
          <div class="card-head">
            <span :class="`status${statusClass}`"><span class="dot"></span>{{ statusLabel }}</span>
            <span v-if="isLimited" class="tag red">Quota</span>
          </div>
          <div class="source-rows">
            <div v-for="item in rowPercents" :key="item.row.key" class="progress-row">
              <div class="row-label">
                <strong>{{ item.row.label }}</strong>
                <span>{{ rowText(item.row) }}</span>
              </div>
              <div class="progress" :class="{ empty: item.percent === null }" :style="barStyle(item.percent, item.row.color)"></div>
              <div class="percent">{{ percentText(item.percent) }}</div>
            </div>
          </div>
        </article>
      </section>

      <p class="footer-note">{{ error ? 'Some data is unavailable. Refresh again later.' : '' }}</p>
    </main>
  </div>
</template>

<style>
:root {
  color-scheme: light;
  --bg: #f6f8fc;
  --card: #ffffff;
  --text: #172033;
  --muted: #7a869a;
  --line: #e7ecf4;
  --blue: #2563eb;
  --cyan: #06b6d4;
  --green: #22c55e;
  --orange: #f59e0b;
  --red: #ef4444;
  --shadow: 0 18px 45px rgba(18, 32, 64, 0.08);
  --radius-xl: 24px;
  --radius-lg: 18px;
}

* {
  box-sizing: border-box;
}

body {
  margin: 0;
  min-height: 100vh;
  background: var(--bg);
  color: var(--text);
  font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}

.app {
  display: grid;
  grid-template-columns: 280px minmax(0, 1fr);
  min-height: 100vh;
}

.sidebar {
  padding: 28px 20px;
  border-right: 1px solid rgba(231, 236, 244, 0.9);
  background: rgba(255, 255, 255, 0.74);
  backdrop-filter: blur(18px);
  position: sticky;
  top: 0;
  height: 100vh;
}

.brand {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 34px;
  padding: 0 8px;
}

.brand-logo {
  width: 44px;
  height: 44px;
  display: grid;
  place-items: center;
  color: white;
  font-weight: 800;
  border-radius: 15px;
  background: linear-gradient(135deg, var(--blue), var(--cyan));
  box-shadow: 0 12px 28px rgba(37, 99, 235, 0.28);
}

.brand strong {
  display: block;
  font-size: 17px;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 12px;
  height: 46px;
  padding: 0 14px;
  color: #5f6b7e;
  border-radius: 15px;
  text-decoration: none;
  font-size: 14px;
  font-weight: 650;
  margin-bottom: 6px;
}

.nav-item svg {
  width: 19px;
  height: 19px;
}

.nav-item.active {
  color: var(--blue);
  background: #edf4ff;
  box-shadow: inset 0 0 0 1px rgba(37, 99, 235, 0.09);
}

.mini-card {
  margin-top: 28px;
  border: 1px solid var(--line);
  border-radius: var(--radius-lg);
  background: linear-gradient(180deg, #ffffff, #f8fbff);
  padding: 16px;
}

.mini-card .small {
  color: var(--muted);
  font-size: 12px;
  line-height: 1.5;
  overflow-wrap: anywhere;
}

.mini-card strong {
  display: block;
  margin-bottom: 6px;
}

.main {
  padding: 28px;
  min-width: 0;
}

.topbar {
  display: flex;
  justify-content: space-between;
  gap: 18px;
  align-items: center;
  margin-bottom: 20px;
}

.title h1 {
  margin: 0;
  font-size: 30px;
  letter-spacing: 0;
}

.title p {
  margin: 7px 0 0;
  color: var(--muted);
  font-size: 14px;
  overflow-wrap: anywhere;
}

.toolbar {
  display: flex;
  gap: 10px;
  align-items: center;
  flex-wrap: wrap;
  justify-content: flex-end;
}

.chip,
.button {
  border: 1px solid var(--line);
  background: rgba(255, 255, 255, 0.78);
  border-radius: 999px;
  padding: 10px 14px;
  color: #526075;
  font-size: 13px;
  font-weight: 700;
  box-shadow: 0 8px 22px rgba(18, 32, 64, 0.04);
  line-height: 1;
  white-space: nowrap;
}

.button {
  border: 0;
  color: white;
  background: linear-gradient(135deg, var(--blue), var(--cyan));
  cursor: pointer;
}

.grid {
  display: grid;
  grid-template-columns: repeat(12, minmax(0, 1fr));
  gap: 18px;
}

.sources {
  margin-top: 18px;
}

.card {
  background: rgba(255, 255, 255, 0.88);
  border: 1px solid rgba(231, 236, 244, 0.94);
  border-radius: var(--radius-xl);
  box-shadow: var(--shadow);
  padding: 20px;
  min-width: 0;
}

.metric-card {
  padding: 16px;
}

.source-card {
  grid-column: span 12;
}

.span-3 {
  grid-column: span 3;
}

.metric-head,
.card-head,
.source-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.metric-head {
  margin-bottom: 12px;
  min-height: 70px;
}

.metric-head > div,
.source-head > div {
  min-width: 0;
}

.card-head {
  margin-bottom: 16px;
}

.source-head {
  margin-bottom: 14px;
}

.eyebrow {
  margin: 0 0 7px;
  color: var(--muted);
  font-size: 13px;
  font-weight: 750;
  letter-spacing: 0.04em;
  line-height: 1;
  text-transform: uppercase;
}

.metric-value {
  font-size: 24px;
  font-weight: 850;
  letter-spacing: 0;
  font-variant-numeric: tabular-nums;
  line-height: 1.15;
  margin: 0;
  overflow-wrap: anywhere;
}

.delta,
.tag,
.status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border-radius: 999px;
  padding: 6px 9px;
  font-size: 12px;
  font-weight: 850;
  line-height: 1;
  white-space: nowrap;
}

.metric-head .delta {
  margin-top: 1px;
}

.delta {
  background: #ecfdf5;
  color: #15803d;
}

.delta.warn {
  background: #fff7ed;
  color: #c2410c;
}

.delta.danger {
  background: #fef2f2;
  color: #b91c1c;
}

.tag.red {
  background: #fef2f2;
  color: #b91c1c;
}

.status {
  color: #0f766e;
  background: #ecfeff;
}

.status.warn {
  color: #c2410c;
  background: #fff7ed;
}

.status.danger {
  color: #b91c1c;
  background: #fef2f2;
}

.status.gray {
  color: #64748b;
  background: #f1f5f9;
}

.dot {
  width: 8px;
  height: 8px;
  border-radius: 999px;
  background: currentColor;
  box-shadow: 0 0 0 5px color-mix(in srgb, currentColor, transparent 84%);
}

.progress {
  --value: 0;
  --bar: var(--blue);
  height: 10px;
  position: relative;
  overflow: hidden;
  border-radius: 999px;
  background: #edf2f8;
}

.progress::after {
  content: "";
  position: absolute;
  inset: 0 auto 0 0;
  width: calc(var(--value) * 1%);
  border-radius: inherit;
  background: linear-gradient(90deg, var(--bar), color-mix(in srgb, var(--bar), white 34%));
  box-shadow: 0 8px 18px color-mix(in srgb, var(--bar), transparent 75%);
  animation: grow 0.9s cubic-bezier(0.2, 0.8, 0.2, 1) both;
}

@keyframes grow {
  from {
    width: 0;
  }
  to {
    width: calc(var(--value) * 1%);
  }
}

.progress.empty::after {
  width: 0;
}

.progress-row {
  display: grid;
  grid-template-columns: minmax(112px, 156px) minmax(0, 1fr) 82px;
  align-items: center;
  gap: 14px;
  padding: 13px 0;
  border-top: 1px solid var(--line);
}

.progress-row:first-child {
  border-top: 0;
  padding-top: 0;
}

.progress-row:last-child {
  padding-bottom: 0;
}

.row-label {
  min-width: 0;
}

.row-label strong {
  display: block;
  font-size: 14px;
  overflow-wrap: anywhere;
}

.row-label span {
  display: block;
  margin-top: 3px;
  color: var(--muted);
  font-size: 12px;
  overflow-wrap: anywhere;
}

.percent {
  font-size: 13px;
  font-weight: 850;
  font-variant-numeric: tabular-nums;
  text-align: right;
}

.source-name {
  margin: 0;
  font-size: 20px;
  letter-spacing: 0;
  overflow-wrap: anywhere;
}

.source-meta {
  margin-top: 7px;
  color: var(--muted);
  font-size: 13px;
  overflow-wrap: anywhere;
}

.source-meta span {
  display: block;
}

.source-score {
  font-size: 32px;
  line-height: 1;
  font-weight: 900;
  font-variant-numeric: tabular-nums;
  text-align: right;
}

.source-score span {
  display: block;
  margin-top: 5px;
  color: var(--muted);
  font-size: 12px;
  font-weight: 800;
}

.source-rows {
  display: grid;
}

.footer-note {
  margin-top: 18px;
  color: var(--muted);
  font-size: 12px;
  text-align: right;
  min-height: 18px;
}

@media (max-width: 1120px) {
  .app {
    grid-template-columns: 1fr;
  }
  .sidebar {
    display: none;
  }
  .span-3,
  .source-card {
    grid-column: span 12;
  }
}

@media (max-width: 720px) {
  .main {
    padding: 18px;
  }
  .topbar {
    align-items: flex-start;
    flex-direction: column;
  }
  .toolbar {
    justify-content: flex-start;
  }
  .progress-row {
    grid-template-columns: 1fr;
    gap: 8px;
  }
  .percent {
    text-align: left;
  }
  .source-head {
    flex-direction: column;
  }
  .source-score {
    text-align: left;
  }
}
</style>
