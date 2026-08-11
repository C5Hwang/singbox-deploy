<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import LatencyTrendModal from "../components/LatencyTrendModal.vue";
import { fetchLatency } from "../api";
import { formatDateTime } from "../utils";
import type { LatencySnapshot, PingTarget, Summary } from "../types";

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

function latestFor(node: NodeLatency, targetID: string) {
  return node.snapshot?.latest.find((p) => p.target === targetID);
}

function latencyText(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return "NA";
  return `${ms.toFixed(ms >= 100 ? 0 : 1)} ms`;
}

// Loss is what turns a plausible latency into an unusable route, so it is the
// value the tiles are toned by.
function lossTone(lossPct: number | undefined): string {
  if (lossPct === undefined) return " gray";
  if (lossPct >= 50) return " danger";
  if (lossPct > 0) return " warn";
  return "";
}

// The card's headline is the node's median reachable probe: a mean would let
// one black-holed carrier speak for the whole node.
function medianLatency(node: NodeLatency): number | null {
  const values = (node.snapshot?.latest ?? []).map((p) => p.avgMs).filter((v): v is number => v !== null).sort((a, b) => a - b);
  if (values.length === 0) return null;
  return values[Math.floor((values.length - 1) / 2)];
}

function reachable(node: NodeLatency): string {
  const latest = node.snapshot?.latest ?? [];
  const up = latest.filter((p) => p.lossPct < 100).length;
  return `${up}/${latest.length || node.snapshot?.targets.length || 0} probes answering`;
}

function sampledAt(node: NodeLatency): string {
  const newest = (node.snapshot?.latest ?? []).reduce((max, p) => Math.max(max, p.ts), 0);
  return newest > 0 ? formatDateTime(newest * 1000) : "";
}

function targetsOf(node: NodeLatency): PingTarget[] {
  return node.snapshot?.targets ?? [];
}

function worstTone(node: NodeLatency): string {
  const latest = node.snapshot?.latest ?? [];
  if (latest.length === 0) return " gray";
  return lossTone(Math.max(...latest.map((p) => p.lossPct)));
}
</script>

<template>
  <section class="grid">
    <article class="card span-12 latency-head">
      <div>
        <p class="eyebrow">Probe target</p>
        <p class="metric-value small">Three carriers · Beijing, Shanghai, Guangzhou</p>
        <p class="metric-detail">
          TCP connect to each carrier's CDN node, five probes every minute, seven days of history.
          Click a node for its trend.
        </p>
      </div>
    </article>
  </section>

  <p v-if="loading && nodes.length === 0" class="no-data">Loading latency data...</p>

  <section class="grid sources" aria-label="latency by node">
    <article
      v-for="node in nodes"
      :key="node.key"
      class="card span-6 node-card"
      :class="{ clickable: !!node.snapshot }"
      @click="node.snapshot && (openNode = node)"
    >
      <div class="rc-head">
        <div class="rc-title">
          <p class="eyebrow">{{ node.name }}</p>
          <p class="metric-value">{{ latencyText(medianLatency(node)) }}</p>
          <p class="metric-detail">
            <span v-if="node.snapshot">median · {{ reachable(node) }}</span>
            <span v-else>unavailable</span>
          </p>
        </div>
        <div class="rc-side">
          <span :class="`status${worstTone(node)}`"><i class="dot"></i>{{ sampledAt(node) || "no data" }}</span>
        </div>
      </div>

      <p v-if="node.error" class="no-data">
        Latency is unavailable for this node: {{ node.error }}. A node still running an older agent does not
        report latency until it is upgraded.
      </p>
      <p v-else-if="node.snapshot && targetsOf(node).length === 0" class="no-data">
        This node reports no latency targets.
      </p>
      <div v-else class="probe-grid">
        <div v-for="target in targetsOf(node)" :key="target.id" class="probe">
          <span class="probe-label">{{ target.carrier }} · {{ target.city }}</span>
          <span class="probe-value">{{ latencyText(latestFor(node, target.id)?.avgMs) }}</span>
          <span :class="`probe-loss${lossTone(latestFor(node, target.id)?.lossPct)}`">
            {{ latestFor(node, target.id) ? `${Math.round(latestFor(node, target.id)!.lossPct)}%` : "—" }}
          </span>
        </div>
      </div>

      <p class="view-trend-row"><span class="view-trend">View trend<svg viewBox="0 0 24 24" fill="none"><path d="M9 6l6 6-6 6" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/></svg></span></p>
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
.latency-head { display: flex; flex-wrap: wrap; align-items: flex-end; justify-content: space-between; gap: 16px; }
.node-card { display: flex; flex-direction: column; }
.probe-grid {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr));
  gap: 8px 14px; margin-top: 18px;
}
.probe {
  display: grid; grid-template-columns: 1fr auto auto; align-items: baseline; gap: 8px;
  padding: 8px 0; border-top: 1px solid var(--line);
}
.probe-label { color: var(--muted); font-size: 12px; font-weight: 650; }
.probe-value { font-size: 14px; font-weight: 800; font-variant-numeric: tabular-nums; }
.probe-loss { font-size: 11px; font-weight: 750; color: #15803d; font-variant-numeric: tabular-nums; }
.probe-loss.warn { color: var(--yellow); }
.probe-loss.danger { color: var(--red); }
.probe-loss.gray { color: var(--muted); }
.view-trend-row { margin: 16px 0 0; display: flex; justify-content: flex-end; }
</style>
