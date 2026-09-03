<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import LatencyMatrix from "../components/LatencyMatrix.vue";
import LatencyTrendModal from "../components/LatencyTrendModal.vue";
import { fetchLatency } from "../api";
import { LATENCY_MISSING, LATENCY_STEPS, LOSS_WARNING } from "../latencyScale";
import { carrierTargets, type LatencySnapshot, type PingLatestPoint, type Summary } from "../types";

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
  // pending is a card that has been asked and has not answered yet. A node that
  // has answered before keeps its previous reading across a refresh instead of
  // going back to this state.
  pending: boolean;
}

const nodes = ref<NodeLatency[]>([]);
const openNode = ref<NodeLatency | null>(null);
// Each round is numbered so a reply from a superseded one — the fleet changed,
// or the node it was waiting on took longer than the poll interval — cannot
// write over the round that replaced it.
let round = 0;

// Every node is fetched in parallel and each card is filled in the moment its
// own node answers. Nothing waits for the slowest one: a spoke that is powered
// off drops its packets rather than refusing them, so it can only fail by
// timing out, and one node's timeout would otherwise be the whole page's.
function load() {
  const targets = sources.value;
  if (targets.length === 0) return;
  const current = ++round;
  const previous = new Map(nodes.value.map((node) => [node.key, node]));
  nodes.value = targets.map((source) => {
    const key = sourceKey(source);
    const name = source.name ?? key;
    const carried = previous.get(key);
    // The reading is carried over; the name is not, so a node renamed between
    // rounds is not labelled with its old alias until it answers.
    return carried ? { ...carried, name } : { key, name, snapshot: null, error: "", pending: true };
  });
  for (const source of targets) {
    const key = sourceKey(source);
    const name = source.name ?? key;
    fetchLatency(key)
      .then((snapshot) => ({ key, name, snapshot, error: "", pending: false }))
      .catch((e) => ({ key, name, snapshot: null, error: e instanceof Error ? e.message : String(e), pending: false }))
      .then((node) => {
        if (current !== round) return;
        const at = nodes.value.findIndex((n) => n.key === key);
        if (at < 0) return;
        nodes.value[at] = node;
        // The modal holds a snapshot by value, so it is re-pointed at the
        // refreshed one rather than left showing the round it was opened on.
        if (openNode.value?.key === key) openNode.value = node;
      });
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

// This page reports the fixed carrier probes only. A node that also relays runs
// probes against its landing nodes, and those belong to the relay page — folding
// them in here would move a node's headline for a reason the matrix never shows.
function carrierLatest(node: NodeLatency): PingLatestPoint[] {
  const snapshot = node.snapshot;
  if (!snapshot) return [];
  const ids = new Set(carrierTargets(snapshot.targets).map((t) => t.id));
  return snapshot.latest.filter((p) => ids.has(p.target));
}

// The card's headline is the node's median reachable probe: a mean would let
// one black-holed carrier speak for the whole node.
function medianLatency(node: NodeLatency): number | null {
  const values = carrierLatest(node)
    .map((p) => p.avgMs)
    .filter((v): v is number => v !== null)
    .sort((a, b) => a - b);
  if (values.length === 0) return null;
  return values[Math.floor((values.length - 1) / 2)];
}

function headline(node: NodeLatency): string {
  if (node.pending) return "…";
  const ms = medianLatency(node);
  return ms === null ? "NA" : `${ms.toFixed(ms >= 100 ? 0 : 1)} ms`;
}

// The headline is a latency, so it is coloured like one — the same four steps
// the matrix underneath fills its cells with, in the grade that reads as text
// on the card. The node's own name stays in the title ink: it is the card's
// subject, not one of its readings.
function headlineColor(node: NodeLatency): string {
  const ms = medianLatency(node);
  if (ms === null) return LATENCY_MISSING.text;
  return LATENCY_STEPS.find((s) => ms < s.limit)!.text;
}

// The dot is the whole status report: green when every probe answered clean,
// amber when something is losing packets, red when a route is down.
function statusTone(node: NodeLatency): string {
  const latest = carrierLatest(node);
  if (node.error || latest.length === 0) return "gray";
  if (latest.some((p) => p.lossPct >= 100)) return "danger";
  if (latest.some((p) => p.lossPct > 0)) return "warn";
  return "ok";
}

function statusLabel(node: NodeLatency): string {
  const latest = carrierLatest(node);
  const answering = latest.filter((p) => p.lossPct < 100).length;
  if (node.pending) return "waiting for this node";
  if (node.error) return "unavailable";
  if (latest.length === 0) return "no data";
  return `${answering} of ${latest.length} probes answering`;
}
</script>

<template>
  <!-- The colour is a second reading of a number that is already printed on
       every cell, so the key is a strip and two words rather than a legend. -->
  <div class="scale">
    <span>faster</span>
    <i v-for="step in LATENCY_STEPS" :key="step.fill" :style="{ background: step.fill }"></i>
    <span>slower</span>
    <em class="scale-loss"><i :style="{ background: LOSS_WARNING }"></i>packet loss</em>
  </div>

  <section class="nodes" aria-label="latency by node">
    <template v-if="nodes.length === 0">
      <article v-for="n in 2" :key="n" class="card node-card skeleton-card" aria-hidden="true">
        <div class="skeleton w40"></div>
        <div class="skeleton w70"></div>
        <div class="skeleton block"></div>
      </article>
    </template>

    <article
      v-for="node in nodes"
      :key="node.key"
      class="card node-card latency-card"
      :class="{ clickable: !!node.snapshot }"
      :title="node.snapshot ? 'Open the latency trend' : ''"
      @click="node.snapshot && (openNode = node)"
    >
      <div class="node-head">
        <div class="node-title">
          <span class="dot-only" :class="statusTone(node)" :title="statusLabel(node)" :aria-label="statusLabel(node)"></span>
          <div>
            <h2 class="node-name">{{ node.name }}</h2>
            <p class="node-meta"><span>{{ statusLabel(node) }}</span></p>
          </div>
        </div>
        <p class="node-latency" :style="{ color: headlineColor(node) }">{{ headline(node) }}</p>
      </div>

      <div v-if="node.pending" class="skeleton-card" aria-hidden="true">
        <div class="skeleton block"></div>
      </div>
      <p v-else-if="node.error" class="no-data">Latency is unavailable for this node.</p>
      <LatencyMatrix v-else-if="node.snapshot" :snapshot="node.snapshot" />
    </article>
  </section>

  <LatencyTrendModal
    v-if="openNode && openNode.snapshot"
    :nodeKey="openNode.key"
    :nodeName="openNode.name"
    :snapshot="openNode.snapshot"
    @close="openNode = null"
  />
</template>

<style scoped>
.scale {
  display: flex; align-items: center; flex-wrap: wrap; gap: 6px;
  margin: 0 2px 12px; color: var(--muted); font-family: var(--font-mono); font-size: 10.5px;
  letter-spacing: 0.06em; text-transform: uppercase;
}
.scale i { width: 24px; height: 6px; border-radius: 2px; }
.scale-loss { display: inline-flex; align-items: center; gap: 6px; margin-left: 12px; font-style: normal; }
.scale-loss i { width: 18px; height: 4px; border-radius: 999px; }
/* The headline is the node's median, printed large and in the ramp's ink. */
.node-latency {
  margin: 0; flex-shrink: 0; font-size: 24px; font-weight: 800; line-height: 1.1;
  letter-spacing: -0.02em; font-variant-numeric: tabular-nums;
  transition: color 0.4s ease;
}
</style>
