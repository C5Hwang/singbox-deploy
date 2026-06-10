<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watchEffect } from "vue";
import SidebarNav from "./components/SidebarNav.vue";
import NetworkTraffic from "./pages/NetworkTraffic.vue";
import Resources from "./pages/Resources.vue";
import { fetchSummary } from "./api";
import type { Summary } from "./types";

const activeTab = ref<"traffic" | "resources">("traffic");
const summary = ref<Summary | null>(null);
const error = ref<string>("");
const now = ref(new Date());
let loadTimer: number | undefined;
let clockTimer: number | undefined;

async function load() {
  try {
    const res = await fetchSummary();
    summary.value = res;
    error.value = "";
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

const clockLabel = computed(() =>
  `${now.value.toLocaleTimeString("en-US", { hour12: false, timeZone: "UTC" })} GMT`,
);

const sourceCount = computed(() => summary.value?.sources?.length ?? 0);
const pageTitle = computed(() => (activeTab.value === "traffic" ? "Network Traffic" : "Resources"));

const subtitle = computed(() => {
  if (error.value) return `Failed to load data: ${error.value}`;
  if (!summary.value) return "Loading monitor data...";
  return "";
});

onMounted(() => {
  load();
  loadTimer = window.setInterval(load, 10000);
  clockTimer = window.setInterval(() => {
    now.value = new Date();
  }, 1000);
});
watchEffect(() => {
  document.title = pageTitle.value;
});
onUnmounted(() => {
  if (loadTimer) window.clearInterval(loadTimer);
  if (clockTimer) window.clearInterval(clockTimer);
});
</script>

<template>
  <div class="app">
    <SidebarNav v-model:activeTab="activeTab" :sourceCount="sourceCount" />

    <main class="main">
      <header class="topbar">
        <div class="title">
          <h1 class="page-title">{{ pageTitle }}</h1>
          <p v-if="subtitle">{{ subtitle }}</p>
        </div>
        <div class="toolbar">
          <div class="mobile-tabs">
            <button :class="{ active: activeTab === 'traffic' }" @click="activeTab = 'traffic'">Traffic</button>
            <button :class="{ active: activeTab === 'resources' }" @click="activeTab = 'resources'">Resources</button>
          </div>
          <div class="chip">{{ clockLabel }}</div>
        </div>
      </header>

      <NetworkTraffic v-if="activeTab === 'traffic'" :summary="summary" :error="error" />
      <Resources v-if="activeTab === 'resources'" :summary="summary" :error="error" />

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

* { box-sizing: border-box; }

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

/* ── Sidebar ──────────────────────────────────────────────── */
.sidebar {
  padding: 28px 20px;
  border-right: 1px solid rgba(231, 236, 244, 0.9);
  background: rgba(255, 255, 255, 0.74);
  backdrop-filter: blur(18px);
  position: sticky;
  top: 0;
  height: 100vh;
}
.brand { display: flex; align-items: center; gap: 12px; margin-bottom: 34px; padding: 0 8px; }
.brand-logo {
  width: 44px; height: 44px; display: grid; place-items: center;
  color: white; font-weight: 800; border-radius: 15px;
  background: linear-gradient(135deg, var(--blue), var(--cyan));
  box-shadow: 0 12px 28px rgba(37, 99, 235, 0.28);
}
.brand strong { display: block; font-size: 17px; }
.nav-item {
  display: flex; align-items: center; gap: 12px; height: 46px; padding: 0 14px;
  color: #5f6b7e; border-radius: 15px; text-decoration: none;
  font-size: 14px; font-weight: 650; margin-bottom: 6px; cursor: pointer;
}
.nav-item svg { width: 19px; height: 19px; flex-shrink: 0; }
.nav-item.active {
  color: var(--blue); background: #edf4ff;
  box-shadow: inset 0 0 0 1px rgba(37, 99, 235, 0.09);
}
.mini-card {
  margin-top: 28px; border: 1px solid var(--line); border-radius: var(--radius-lg);
  background: linear-gradient(180deg, #ffffff, #f8fbff); padding: 16px;
}
.mini-card strong { display: block; }

/* ── Main ─────────────────────────────────────────────────── */
.main { padding: 28px; min-width: 0; }
.topbar { display: flex; justify-content: space-between; gap: 18px; align-items: center; margin-bottom: 20px; }
.title h1 { margin: 0; font-size: 30px; letter-spacing: -0.02em; }
.page-title { font-weight: 900; font-size: 32px; letter-spacing: -0.03em; line-height: 1.1; }
.title p { margin: 7px 0 0; color: var(--muted); font-size: 14px; overflow-wrap: anywhere; }
.toolbar { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; justify-content: flex-end; }
.chip {
  border: 1px solid var(--line); background: rgba(255, 255, 255, 0.78);
  border-radius: 999px; padding: 10px 14px; color: #526075;
  font-size: 13px; font-weight: 700; box-shadow: 0 8px 22px rgba(18, 32, 64, 0.04);
  line-height: 1; white-space: nowrap;
}

/* ── Grid & Cards ─────────────────────────────────────────── */
.grid { display: grid; grid-template-columns: repeat(12, minmax(0, 1fr)); gap: 18px; }
.sources { margin-top: 18px; }
.card {
  background: rgba(255, 255, 255, 0.88); border: 1px solid rgba(231, 236, 244, 0.94);
  border-radius: var(--radius-xl); box-shadow: var(--shadow); padding: 20px; min-width: 0;
  animation: cardIn 0.45s cubic-bezier(0.2, 0.8, 0.2, 1) both;
}
.grid > .card:nth-child(2) { animation-delay: 0.05s; }
.grid > .card:nth-child(3) { animation-delay: 0.1s; }
.grid > .card:nth-child(4) { animation-delay: 0.15s; }
@keyframes cardIn { from { opacity: 0; transform: translateY(12px); } }
.metric-card { padding: 16px; display: flex; flex-direction: column; }
.metric-card > .progress { margin-top: auto; }
.source-card { grid-column: span 12; }
.span-3 { grid-column: span 3; }
.span-4 { grid-column: span 4; }
.span-6 { grid-column: span 6; }
.clickable { cursor: pointer; transition: transform 0.15s, box-shadow 0.15s; }
.clickable:hover { transform: translateY(-2px); box-shadow: 0 22px 50px rgba(18, 32, 64, 0.12); }

.metric-head {
  display: flex; align-items: flex-start; justify-content: space-between;
  gap: 12px; margin-bottom: 12px; min-height: 70px;
}
.metric-head > div { min-width: 0; }

.eyebrow {
  margin: 0 0 7px; color: var(--muted); font-size: 13px; font-weight: 750;
  letter-spacing: 0.04em; line-height: 1; text-transform: uppercase;
}
.metric-value {
  font-size: 24px; font-weight: 850; letter-spacing: 0;
  font-variant-numeric: tabular-nums; line-height: 1.15; margin: 0; overflow-wrap: anywhere;
}
.metric-value.small { font-size: 18px; }
.metric-detail { margin: 4px 0 0; min-height: 14px; color: var(--muted); font-size: 12px; font-weight: 700; font-variant-numeric: tabular-nums; }

/* ── Badges ───────────────────────────────────────────────── */
.delta, .tag, .status {
  display: inline-flex; align-items: center; gap: 6px;
  border-radius: 999px; padding: 6px 9px;
  font-size: 12px; font-weight: 850; line-height: 1; white-space: nowrap;
}
.metric-head .delta { margin-top: 1px; }
.delta { background: #ecfdf5; color: #15803d; }
.delta.warn { background: #fff7ed; color: #c2410c; }
.delta.danger { background: #fef2f2; color: #b91c1c; }
.tag.red { background: #fef2f2; color: #b91c1c; }
.status { color: #0f766e; background: #ecfeff; }
.status.warn { color: #c2410c; background: #fff7ed; }
.status.danger { color: #b91c1c; background: #fef2f2; }
.status.gray { color: #64748b; background: #f1f5f9; }
.dot {
  width: 8px; height: 8px; border-radius: 999px; background: currentColor;
  animation: pulseDot 2.4s ease-in-out infinite;
}
.status.gray .dot { animation: none; box-shadow: 0 0 0 4px color-mix(in srgb, currentColor, transparent 84%); }
@keyframes pulseDot {
  0%, 100% { box-shadow: 0 0 0 3px color-mix(in srgb, currentColor, transparent 82%); }
  50% { box-shadow: 0 0 0 7px color-mix(in srgb, currentColor, transparent 93%); }
}

.view-trend {
  margin-left: auto; display: inline-flex; align-items: center; gap: 5px; flex-shrink: 0;
  border: 1px solid var(--line); border-radius: 999px; padding: 7px 12px;
  font-size: 12px; font-weight: 700; line-height: 1; color: var(--muted);
  background: rgba(255, 255, 255, 0.7); white-space: nowrap;
  transition: color 0.15s, border-color 0.15s, background 0.15s;
}
.view-trend svg { width: 13px; height: 13px; transition: transform 0.2s; }
.clickable:hover .view-trend {
  color: var(--blue); background: #edf4ff;
  border-color: color-mix(in srgb, var(--blue), transparent 65%);
}
.clickable:hover .view-trend svg { transform: translateX(2px); }

/* ── Progress bar ─────────────────────────────────────────── */
.progress {
  --value: 0; --bar: var(--blue);
  height: 10px; position: relative; overflow: hidden;
  border-radius: 999px; background: #edf2f8;
}
.progress::after {
  content: ""; position: absolute; inset: 0 auto 0 0;
  width: calc(var(--value) * 1%); border-radius: inherit;
  background: linear-gradient(90deg, var(--bar), color-mix(in srgb, var(--bar), white 34%));
  box-shadow: 0 8px 18px color-mix(in srgb, var(--bar), transparent 75%);
  animation: grow 0.9s cubic-bezier(0.2, 0.8, 0.2, 1) both;
}
@keyframes grow { from { width: 0 } to { width: calc(var(--value) * 1%) } }
.progress.empty::after { width: 0; }

/* ── Traffic card ─────────────────────────────────────────── */
.source-name { margin: 0; font-size: 20px; letter-spacing: 0; overflow-wrap: anywhere; }
.tc-body { display: flex; align-items: center; gap: 16px 40px; margin-top: 20px; }
.tc-body > .gauge { flex-shrink: 0; }
.usage-rows { flex: 1; min-width: 0; display: grid; }
.usage-row {
  display: grid; grid-template-columns: minmax(112px, 170px) minmax(0, 1fr) 80px;
  align-items: center; gap: 14px; padding: 13px 0; border-top: 1px solid var(--line);
}
.usage-row:first-child { border-top: 0; padding-top: 0; }
.usage-row:last-child { padding-bottom: 0; }
.row-label { min-width: 0; }
.row-label strong { display: block; font-size: 14px; overflow-wrap: anywhere; }
.row-label span {
  display: block; margin-top: 3px; color: var(--muted); font-size: 12px;
  overflow-wrap: anywhere; font-variant-numeric: tabular-nums;
}
.percent { font-size: 13px; font-weight: 850; font-variant-numeric: tabular-nums; text-align: right; }
.percent.warn { color: var(--orange); }
.percent.danger { color: var(--red); }

/* ── Resource card ────────────────────────────────────────── */
.rc-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.rc-title { min-width: 0; }
.rc-side { display: flex; align-items: center; gap: 10px; flex-shrink: 0; }
.rc-body {
  display: flex; flex-wrap: wrap; align-items: center;
  justify-content: space-between; gap: 20px 28px; margin-top: 20px;
}
.gauges { flex: 1 1 420px; display: flex; flex-wrap: wrap; justify-content: space-around; gap: 18px 30px; }
.gauge { display: flex; flex-direction: column; align-items: center; text-align: center; min-width: 104px; }
.ring-wrap { position: relative; width: 98px; height: 98px; }
.ring { width: 100%; height: 100%; transform: rotate(-90deg); }
.ring-bg { fill: none; stroke: #edf2f8; stroke-width: 8; }
.ring-fg {
  fill: none; stroke-width: 8; stroke-linecap: round;
  stroke-dasharray: 213.63;
  transition: stroke-dashoffset 0.8s cubic-bezier(0.2, 0.8, 0.2, 1), stroke 0.3s;
  animation: ringIn 1s cubic-bezier(0.2, 0.8, 0.2, 1) both;
}
@keyframes ringIn { from { stroke-dashoffset: 213.63; } }
.ring-value {
  position: absolute; inset: 0; display: grid; place-items: center;
  font-size: 16px; font-weight: 850; font-variant-numeric: tabular-nums;
}
.ring-value.warn { color: var(--orange); }
.ring-value.danger { color: var(--red); }
.ring-value.infinite { font-size: 24px; font-weight: 800; color: var(--muted); }
.gauge-label {
  margin-top: 9px; color: var(--muted); font-size: 12px; font-weight: 750;
  text-transform: uppercase; letter-spacing: 0.04em;
}
.gauge-detail {
  margin-top: 3px; min-height: 14px; color: var(--muted);
  font-size: 11px; font-weight: 700; font-variant-numeric: tabular-nums;
}
.io-block { display: flex; flex-direction: column; gap: 10px; min-width: 170px; }
.io-stat {
  display: flex; align-items: center; gap: 11px;
  border: 1px solid var(--line); border-radius: 14px; padding: 10px 14px;
  background: linear-gradient(180deg, #ffffff, #f8fbff);
}
.io-icon {
  width: 30px; height: 30px; display: grid; place-items: center;
  border-radius: 10px; font-size: 15px; font-weight: 800; flex-shrink: 0;
}
.io-icon.read { background: #ecfdf5; color: #15803d; }
.io-icon.write { background: #fff7ed; color: #c2410c; }
.io-label {
  display: block; margin-bottom: 2px; color: var(--muted);
  font-size: 11px; font-weight: 750; text-transform: uppercase; letter-spacing: 0.04em;
}
.io-stat strong { font-size: 15px; font-variant-numeric: tabular-nums; }
.rc-meta {
  display: flex; flex-wrap: wrap; gap: 6px 22px; margin-top: 20px; padding-top: 14px;
  border-top: 1px solid var(--line); color: var(--muted); font-size: 12px; font-weight: 600;
}
.no-data { color: var(--muted); font-size: 14px; padding: 12px 0; }

/* ── Modal ────────────────────────────────────────────────── */
.modal-backdrop {
  position: fixed; inset: 0; z-index: 1000;
  background: rgba(0, 0, 0, 0.45); backdrop-filter: blur(4px);
  display: grid; place-items: center;
  animation: fadeIn 0.2s ease;
}
.modal-content {
  position: relative; background: white; border-radius: var(--radius-xl);
  box-shadow: 0 25px 60px rgba(0, 0, 0, 0.15);
  width: min(92vw, 1000px); max-height: 88vh; overflow: hidden;
  animation: slideUp 0.35s cubic-bezier(0.2, 0.8, 0.2, 1);
}
@keyframes fadeIn { from { opacity: 0 } to { opacity: 1 } }
@keyframes slideUp { from { opacity: 0; transform: translateY(30px) } to { opacity: 1; transform: translateY(0) } }

.modal-header {
  display: flex; justify-content: space-between; align-items: flex-start;
  padding: 24px 64px 12px 28px; gap: 16px;
}
.modal-title { margin: 0; font-size: 20px; }
.modal-subtitle { margin: 4px 0 0; color: var(--muted); font-size: 14px; }
.modal-controls { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }

.toggle-group {
  display: inline-flex; border: 1px solid var(--line); border-radius: 10px; overflow: hidden;
}
.toggle-group button {
  border: none; background: transparent; padding: 8px 14px;
  font-size: 13px; font-weight: 700; cursor: pointer; color: var(--muted);
  transition: background 0.15s, color 0.15s;
}
.toggle-group button.active { background: var(--blue); color: white; }
.toggle-group button:hover:not(.active) { background: #f0f4f8; }

.close-btn {
  position: absolute; top: 18px; right: 18px; z-index: 1;
  width: 34px; height: 34px; display: grid; place-items: center; padding: 0;
  border: none; border-radius: 999px; background: #f1f5f9;
  font-size: 20px; line-height: 1; cursor: pointer; color: var(--muted);
  transition: background 0.15s, color 0.15s;
}
.close-btn:hover { background: #e2e8f0; color: var(--text); }

.chart-container { width: 100%; height: 460px; padding: 8px 16px 20px; }
.chart-loading { padding: 60px; text-align: center; color: var(--muted); font-size: 15px; }

/* ── Mobile tabs ──────────────────────────────────────────── */
.mobile-tabs { display: none; }
.mobile-tabs button {
  border: 1px solid var(--line); background: transparent; border-radius: 10px;
  padding: 8px 16px; font-size: 13px; font-weight: 700; cursor: pointer; color: var(--muted);
}
.mobile-tabs button.active { background: var(--blue); color: white; border-color: var(--blue); }

.footer-note { margin-top: 18px; color: var(--muted); font-size: 12px; text-align: right; min-height: 18px; }

/* ── Responsive ───────────────────────────────────────────── */
@media (max-width: 1120px) {
  .app { grid-template-columns: 1fr; }
  .sidebar { display: none; }
  .mobile-tabs { display: flex; gap: 8px; }
  .span-3, .span-4, .span-6, .source-card { grid-column: span 12; }
}
@media (max-width: 720px) {
  .main { padding: 18px; }
  .topbar { align-items: flex-start; flex-direction: column; }
  .toolbar { justify-content: flex-start; width: 100%; }
  .tc-body { flex-direction: column; align-items: stretch; gap: 14px; }
  .tc-body > .gauge { align-self: center; }
  .usage-row {
    grid-template-columns: minmax(0, 1fr) 64px;
    grid-template-areas: "label label" "bar pct";
    gap: 8px 12px;
  }
  .usage-row .row-label { grid-area: label; }
  .usage-row .progress { grid-area: bar; }
  .usage-row .percent { grid-area: pct; }
  .modal-content { width: 98vw; }
  .modal-header { flex-direction: column; padding: 16px 56px 8px 16px; }
  .modal-controls { width: 100%; }
  .toggle-group {
    max-width: 100%; overflow-x: auto; -webkit-overflow-scrolling: touch;
    scrollbar-width: none;
  }
  .toggle-group::-webkit-scrollbar { display: none; }
  .toggle-group button { padding: 7px 11px; font-size: 12px; flex-shrink: 0; white-space: nowrap; }
  .close-btn { top: 12px; right: 12px; }
  .chart-container { height: 380px; padding: 4px 6px 12px; }
  .rc-head { flex-direction: column; }
  .rc-side { width: 100%; }
  .rc-side .view-trend { margin-left: auto; }
  .gauges { gap: 14px 10px; }
  .gauge { min-width: 92px; }
  .ring-wrap { width: 84px; height: 84px; }
  .ring-value { font-size: 14px; }
  .io-block { flex-direction: row; width: 100%; min-width: 0; }
  .io-stat { flex: 1; min-width: 0; }
}
</style>
