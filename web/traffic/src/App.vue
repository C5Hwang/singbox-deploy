<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";

interface HourlyPoint {
  hourTs: number;
  bytes: number;
}

interface Summary {
  usedBytes: number;
  remainingBytes: number;
  limitBytes: number;
  resetTime: string;
  trend: HourlyPoint[];
}

const summary = ref<Summary | null>(null);
const error = ref<string>("");
let timer: number | undefined;

// The UI is served under /traffic/, so the API is reached via a relative path
// that resolves to /traffic/api/summary.
async function load() {
  try {
    const res = await fetch("api/summary");
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    summary.value = await res.json();
    error.value = "";
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

function formatBytes(n: number): string {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  const i = Math.floor(Math.log(n) / Math.log(1024));
  return `${(n / Math.pow(1024, i)).toFixed(2)} ${units[i]}`;
}

const resetLabel = computed(() =>
  summary.value ? new Date(summary.value.resetTime).toLocaleString() : "—",
);

const limitLabel = computed(() =>
  summary.value && summary.value.limitBytes > 0
    ? formatBytes(summary.value.limitBytes)
    : "Unlimited",
);

// Build an SVG polyline for the trend sparkline.
const trendPath = computed(() => {
  const t = summary.value?.trend ?? [];
  if (t.length < 2) return "";
  const max = Math.max(...t.map((p) => p.bytes), 1);
  const w = 600;
  const h = 120;
  return t
    .map((p, i) => {
      const x = (i / (t.length - 1)) * w;
      const y = h - (p.bytes / max) * h;
      return `${i === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
});

onMounted(() => {
  load();
  timer = window.setInterval(load, 30000);
});
onUnmounted(() => {
  if (timer) window.clearInterval(timer);
});
</script>

<template>
  <main>
    <h1>Traffic Monitor</h1>
    <p v-if="error" class="error">Failed to load: {{ error }}</p>

    <section v-if="summary" class="cards">
      <div class="card">
        <span class="label">Used this cycle</span>
        <span class="value">{{ formatBytes(summary.usedBytes) }}</span>
      </div>
      <div class="card">
        <span class="label">Remaining</span>
        <span class="value">{{ formatBytes(summary.remainingBytes) }}</span>
      </div>
      <div class="card">
        <span class="label">Limit</span>
        <span class="value">{{ limitLabel }}</span>
      </div>
      <div class="card">
        <span class="label">Resets</span>
        <span class="value small">{{ resetLabel }}</span>
      </div>
    </section>

    <section v-if="summary && summary.trend.length > 1" class="trend">
      <h2>Recent trend</h2>
      <svg viewBox="0 0 600 120" preserveAspectRatio="none" class="spark">
        <path :d="trendPath" fill="none" stroke="#3b82f6" stroke-width="2" />
      </svg>
    </section>
  </main>
</template>

<style>
:root {
  color-scheme: light dark;
}
body {
  margin: 0;
  font-family: system-ui, sans-serif;
  background: #0f172a;
  color: #e2e8f0;
}
main {
  max-width: 760px;
  margin: 0 auto;
  padding: 2rem 1.25rem;
}
h1 {
  font-size: 1.6rem;
  margin: 0 0 1.5rem;
}
.error {
  color: #f87171;
}
.cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 1rem;
}
.card {
  background: #1e293b;
  border: 1px solid #334155;
  border-radius: 14px;
  padding: 1rem 1.25rem;
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
}
.label {
  font-size: 0.8rem;
  color: #94a3b8;
}
.value {
  font-size: 1.5rem;
  font-weight: 600;
}
.value.small {
  font-size: 1rem;
  font-weight: 500;
}
.trend {
  margin-top: 2rem;
}
.spark {
  width: 100%;
  height: 120px;
  background: #1e293b;
  border: 1px solid #334155;
  border-radius: 14px;
}
</style>
