<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import LatencyMatrix from "../components/LatencyMatrix.vue";
import LatencyTrendModal from "../components/LatencyTrendModal.vue";
import { fetchLatency } from "../api";
import type { LatencySnapshot, Summary } from "../types";

const props = defineProps<{ summary: Summary | null }>();

// A single-node install reports no source list; it is still one node, spelled
// the way the traffic page spells it.
const sources = computed(() => {
  const list = props.summary?.sources ?? [];
  if (list.length > 0) return list;
  return props.summary ? [{ id: "local", name: "Local Server" }] : [];
});

function sourceKey(source: { id?: string; name?: string }): string {
  return source.id || source.name || "";
}

interface NodeLatency {
  key: string;
  name: string;
  snapshot: LatencySnapshot | null;
  error: string;
}

const nodes = ref<NodeLatency[]>([]);
const loading = ref(false);
const openNode = ref<NodeLatency | null>(null);

// Every node is fetched in parallel and gets its own card, so one unreachable
// spoke costs its own card rather than the page.
async function load() {
  const targets = sources.value;
  if (targets.length === 0) return;
  loading.value = true;
  nodes.value = await Promise.all(
    targets.map(async (source) => {
      const key = sourceKey(source);
      try {
        return { key, name: source.name ?? key, snapshot: await fetchLatency(key), error: "" };
      } catch (e) {
        return { key, name: source.name ?? key, snapshot: null, error: e instanceof Error ? e.message : String(e) };
      }
    }),
  );
  loading.value = false;
  // The modal holds a snapshot by value, so it is re-pointed at the refreshed
  // one rather than left showing the round it was opened on.
  if (openNode.value) {
    openNode.value = nodes.value.find((n) => n.key === openNode.value?.key) ?? null;
  }
}

watch(() => sources.value.map(sourceKey).join(","), load, { immediate: true });

// Probes run every minute, and the card only shows the newest round.
let refreshTimer: number | undefined;
onMounted(() => {
  refreshTimer = window.setInterval(load, 60000);
});
onUnmounted(() => {
  if (refreshTimer) window.clearInterval(refreshTimer);
});

// The card's headline is the node's median reachable probe: a mean would let
// one black-holed carrier speak for the whole node.
function medianLatency(node: NodeLatency): number | null {
  const values = (node.snapshot?.latest ?? [])
    .map((p) => p.avgMs)
    .filter((v): v is number => v !== null)
    .sort((a, b) => a - b);
  if (values.length === 0) return null;
  return values[Math.floor((values.length - 1) / 2)];
}

function headline(node: NodeLatency): string {
  const ms = medianLatency(node);
  return ms === null ? "NA" : `${ms.toFixed(ms >= 100 ? 0 : 1)} ms`;
}

// The dot is the whole status report: green when every probe answered clean,
// amber when something is losing packets, red when a route is down.
function statusTone(node: NodeLatency): string {
  const latest = node.snapshot?.latest ?? [];
  if (node.error || latest.length === 0) return "gray";
  if (latest.some((p) => p.lossPct >= 100)) return "danger";
  if (latest.some((p) => p.lossPct > 0)) return "warn";
  return "ok";
}

function statusLabel(node: NodeLatency): string {
  const latest = node.snapshot?.latest ?? [];
  const answering = latest.filter((p) => p.lossPct < 100).length;
  if (node.error) return "unavailable";
  if (latest.length === 0) return "no data";
  return `${answering} of ${latest.length} probes answering`;
}
</script>

<template>
  <p v-if="loading && nodes.length === 0" class="no-data">Loading latency data...</p>

  <!-- The colour is a second reading of a number that is already printed on
       every cell, so the key is a strip and two words rather than a legend. -->
  <div v-if="nodes.length" class="scale">
    <span>faster</span>
    <i v-for="step in ['#86b6ef', '#3987e5', '#256abf', '#104281']" :key="step" :style="{ background: step }"></i>
    <span>slower</span>
  </div>

  <section class="grid" aria-label="latency by node">
    <article
      v-for="node in nodes"
      :key="node.key"
      class="card span-6 node-card"
      :class="{ clickable: !!node.snapshot }"
      :title="node.snapshot ? 'Open the latency trend' : ''"
      @click="node.snapshot && (openNode = node)"
    >
      <div class="head">
        <div class="title">
          <p class="eyebrow">{{ node.name }}</p>
          <p class="metric-value">{{ headline(node) }}</p>
        </div>
        <span class="dot-only" :class="statusTone(node)" :title="statusLabel(node)" :aria-label="statusLabel(node)"></span>
      </div>

      <p v-if="node.error" class="no-data">Latency is unavailable for this node.</p>
      <LatencyMatrix v-else-if="node.snapshot" :snapshot="node.snapshot" />
    </article>
  </section>

  <LatencyTrendModal
    v-if="openNode && openNode.snapshot"
    :nodeName="openNode.name"
    :snapshot="openNode.snapshot"
    @close="openNode = null"
  />
</template>

<style scoped>
.scale {
  display: flex; align-items: center; gap: 6px;
  margin: 0 2px 12px; color: var(--muted); font-size: 11px; font-weight: 700;
  letter-spacing: 0.03em; text-transform: uppercase;
}
.scale i { width: 26px; height: 7px; border-radius: 2px; }
.node-card { display: flex; flex-direction: column; }
.head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.title .metric-value { margin-top: 4px; font-size: 28px; }
/* The corner carries a state, and a state is a dot. The words that were here
   said what the matrix underneath already shows. */
.dot-only {
  width: 10px; height: 10px; border-radius: 999px; flex-shrink: 0; margin-top: 6px;
  position: relative;
}
.dot-only::before {
  content: ""; position: absolute; inset: 0; border-radius: inherit;
  background: currentColor; animation: pulseDot 2.4s ease-in-out infinite;
}
.dot-only.ok { background: #0ca30c; color: #0ca30c; }
.dot-only.warn { background: #fab219; color: #fab219; }
.dot-only.danger { background: #d03b3b; color: #d03b3b; }
.dot-only.gray { background: #98a2b3; color: #98a2b3; }
.dot-only.gray::before { animation: none; }
@media (prefers-reduced-motion: reduce) {
  .dot-only::before { animation: none; }
}
</style>
