<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import RelayTrendModal from "../components/RelayTrendModal.vue";
import { fetchLatency } from "../api";
import { LATENCY_DEAD, LATENCY_MISSING, LATENCY_STEPS, LOSS_CLEAR, LOSS_CRITICAL, LOSS_WARNING } from "../latencyScale";
import { relayTargets, type LatencySnapshot, type PingLatestPoint, type Summary } from "../types";

// One card per relay: the pairs it forwards, with the latency it measures to
// each landing node. The probe runs on the relay itself, so this is the route
// the relayed traffic actually takes — a client's own latency is that plus its
// hop to the relay.
const props = defineProps<{ summary: Summary | null }>();

const sources = computed(() => {
  const list = props.summary?.sources ?? [];
  if (list.length > 0) return list;
  return props.summary ? [{ id: "local", name: "Local Server" }] : [];
});

function sourceKey(source: { id?: string; name?: string }): string {
  return source.id || source.name || "";
}

interface RelayNode {
  key: string;
  name: string;
  snapshot: LatencySnapshot;
}

const relays = ref<RelayNode[]>([]);
const loading = ref(false);
const openRelay = ref<RelayNode | null>(null);

// Every node is asked, and only the ones that answer with relay probes get a
// card: a node that forwards for nobody has nothing to show here, and a node
// that cannot be reached is already reported on the latency page.
async function load() {
  const targets = sources.value;
  if (targets.length === 0) return;
  loading.value = true;
  const answered = await Promise.all(
    targets.map(async (source) => {
      const key = sourceKey(source);
      try {
        const snapshot = await fetchLatency(key);
        if (relayTargets(snapshot.targets).length === 0) return null;
        return { key, name: source.name ?? key, snapshot };
      } catch {
        return null;
      }
    }),
  );
  relays.value = answered.filter((node): node is RelayNode => node !== null);
  loading.value = false;
  if (openRelay.value) {
    openRelay.value = relays.value.find((n) => n.key === openRelay.value?.key) ?? null;
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

interface Pair {
  id: string;
  name: string;
  // host is the landing node's own address, which is what the relay forwards
  // to and what the client's SNI still names. The probe port is dropped: it is
  // always the same one and says nothing about this pair.
  host: string;
  ms: number | null;
  loss: number | null;
  text: string;
  lossText: string;
  style: Record<string, string>;
  title: string;
}

function pairs(node: RelayNode): Pair[] {
  return relayTargets(node.snapshot.targets).map((target) => {
    const latest: PingLatestPoint | undefined = node.snapshot.latest.find((p) => p.target === target.id);
    const ms = latest?.avgMs ?? null;
    const loss = latest ? latest.lossPct : null;
    const tone = ms === null ? (latest ? LATENCY_DEAD : LATENCY_MISSING) : LATENCY_STEPS.find((s) => ms < s.limit)!;
    const text = ms === null ? (latest ? "out" : "—") : ms >= 100 ? String(Math.round(ms)) : ms.toFixed(1);
    const name = target.name || target.id;
    return {
      id: target.id,
      name,
      host: target.address.replace(/:\d+$/, ""),
      ms,
      loss,
      text,
      // The zero is printed like every other figure: a number that only appears
      // when something is wrong makes its absence ambiguous.
      lossText: loss === null ? "—" : `${Math.round(loss)}%`,
      style: {
        "--fill": tone.fill,
        "--ink": tone.ink,
        "--loss": `${Math.max(0, Math.min(100, loss ?? 0))}%`,
        "--loss-color": (loss ?? 0) >= 50 ? LOSS_CRITICAL : LOSS_WARNING,
        "--loss-ink": loss ? ((loss >= 50 ? LOSS_CRITICAL : LOSS_WARNING) as string) : LOSS_CLEAR,
      },
      title: `${node.name} → ${name} — ${ms === null ? "no answer" : `${text} ms`}, ${
        loss === null ? "no data" : `${Math.round(loss)}% loss`
      }`,
    };
  });
}

function median(values: number[]): number | null {
  if (values.length === 0) return null;
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.floor((sorted.length - 1) / 2)];
}

function msText(ms: number | null): string {
  return ms === null ? "NA" : `${ms.toFixed(ms >= 100 ? 0 : 1)} ms`;
}

function msColor(ms: number | null): string {
  return ms === null ? LATENCY_MISSING.text : LATENCY_STEPS.find((s) => ms < s.limit)!.text;
}

// The card's headline is the relay's median reachable landing node, so one
// unreachable landing node cannot speak for the whole relay.
function medianLatency(node: RelayNode): number | null {
  return median(pairs(node).map((p) => p.ms).filter((v): v is number => v !== null));
}

function pairCount(node: RelayNode): string {
  const count = relayTargets(node.snapshot.targets).length;
  return `${count} landing node${count === 1 ? "" : "s"}`;
}

// One dot for the whole relay: green when every landing answered clean, amber
// when something is losing packets, red when a route is down.
function statusTone(node: RelayNode): string {
  const list = pairs(node);
  if (list.length === 0) return "gray";
  if (list.some((p) => p.ms === null || (p.loss ?? 0) >= 100)) return "danger";
  if (list.some((p) => (p.loss ?? 0) > 0)) return "warn";
  return "ok";
}

function statusLabel(node: RelayNode): string {
  const list = pairs(node);
  const answering = list.filter((p) => p.ms !== null).length;
  return `${answering} of ${list.length} landing nodes answering`;
}

// The strip above the cards answers the questions a fleet-wide reader has
// before they look at any single relay: how much of the fleet is relayed at
// all, how far the hop is, and whether every pair is currently up.
const overview = computed(() => {
  const all = relays.value.flatMap(pairs);
  const answering = all.filter((p) => p.ms !== null).length;
  return {
    relays: relays.value.length,
    landings: all.length,
    hop: median(all.map((p) => p.ms).filter((v): v is number => v !== null)),
    worst: all.length ? Math.max(...all.map((p) => p.ms ?? Infinity)) : null,
    answering,
  };
});

const worstText = computed(() => {
  const worst = overview.value.worst;
  if (worst === null) return "";
  return worst === Infinity ? "one landing node is not answering" : `slowest pair ${msText(worst)}`;
});

// A lone relay takes the full row rather than sitting in half of one with the
// other half blank; its pairs then lay out as a grid instead of one long
// column, so a wide card fills with content rather than with air.
const cardSpan = computed(() => (relays.value.length === 1 ? "span-12" : "span-6"));
</script>

<template>
  <p v-if="loading && relays.length === 0" class="no-data">Loading relay latency data...</p>
  <!-- The navigation only offers this page once something is relayed, so an
       empty one means the relays exist but none of them has reported a round —
       most often because the relay's own monitor is switched off. -->
  <p v-else-if="relays.length === 0" class="no-data">
    No relay is reporting yet. A relay measures the route to each landing node it forwards to, and its readings appear
    here once its monitor has run a round.
  </p>

  <template v-if="relays.length">
    <section class="grid" aria-label="relay overview">
      <article class="card metric-card span-3">
        <div class="metric-head">
          <div>
            <p class="eyebrow">Relays</p>
            <p class="metric-value">{{ overview.relays }}</p>
            <p class="metric-detail">carrying other nodes' traffic</p>
          </div>
        </div>
      </article>

      <article class="card metric-card span-3">
        <div class="metric-head">
          <div>
            <p class="eyebrow">Fronted nodes</p>
            <p class="metric-value">{{ overview.landings }}</p>
            <p class="metric-detail">published under a relay address</p>
          </div>
        </div>
      </article>

      <article class="card metric-card span-3">
        <div class="metric-head">
          <div>
            <p class="eyebrow">Median hop</p>
            <p class="metric-value" :style="{ color: msColor(overview.hop) }">{{ msText(overview.hop) }}</p>
            <p class="metric-detail">{{ worstText }}</p>
          </div>
        </div>
      </article>

      <article class="card metric-card span-3">
        <div class="metric-head">
          <div>
            <p class="eyebrow">Answering</p>
            <p class="metric-value" :class="{ degraded: overview.answering < overview.landings }">
              {{ overview.answering }} / {{ overview.landings }}
            </p>
            <p class="metric-detail">pairs reached on the last round</p>
          </div>
        </div>
      </article>
    </section>

    <div class="scale">
      <span>faster</span>
      <i v-for="step in LATENCY_STEPS" :key="step.fill" :style="{ background: step.fill }"></i>
      <span>slower</span>
      <em class="scale-loss"><i :style="{ background: LOSS_WARNING }"></i>packet loss</em>
    </div>

    <section class="grid sources" aria-label="relay to landing latency">
      <article
        v-for="node in relays"
        :key="node.key"
        class="card node-card clickable"
        :class="cardSpan"
        title="Open the relay latency trend"
        @click="openRelay = node"
      >
        <div class="head">
          <div class="node-title">
            <p class="node-name">{{ node.name }}</p>
            <p class="node-latency" :style="{ color: msColor(medianLatency(node)) }">{{ msText(medianLatency(node)) }}</p>
          </div>
          <div class="head-side">
            <span class="pair-count">{{ pairCount(node) }}</span>
            <span class="dot-only" :class="statusTone(node)" :title="statusLabel(node)" :aria-label="statusLabel(node)"></span>
          </div>
        </div>

        <div class="pairs" role="table" aria-label="Latency to each landing node, milliseconds">
          <div v-for="pair in pairs(node)" :key="pair.id" class="pair" role="row">
            <span class="pair-title" role="rowheader">
              <span class="pair-name" :title="pair.name">{{ pair.name }}</span>
              <span class="pair-host" :title="pair.host">{{ pair.host }}</span>
            </span>
            <span class="cell" :style="pair.style" :title="pair.title" role="cell">
              <span class="value">{{ pair.text }}</span>
              <span class="loss">
                <i class="track" aria-hidden="true"><i class="fill"></i></i>
                <span class="pct">{{ pair.lossText }}</span>
              </span>
            </span>
          </div>
        </div>
      </article>
    </section>
  </template>

  <RelayTrendModal
    v-if="openRelay"
    :nodeKey="openRelay.key"
    :nodeName="openRelay.name"
    :snapshot="openRelay.snapshot"
    @close="openRelay = null"
  />
</template>

<style scoped>
.scale {
  display: flex; align-items: center; gap: 6px;
  margin: 18px 2px 12px; color: var(--muted); font-size: 11px; font-weight: 700;
  letter-spacing: 0.03em; text-transform: uppercase;
}
.scale i { width: 26px; height: 7px; border-radius: 2px; }
.scale-loss { display: inline-flex; align-items: center; gap: 6px; margin-left: 14px; font-style: normal; }
.scale-loss i { width: 18px; height: 4px; border-radius: 999px; }
/* The strip is four readings of the same subject, so all four cards are the
   same shape. The metric cards elsewhere carry a quota bar; none of these four
   has a denominator worth drawing, and a bar on one of them would read as the
   odd one out rather than as information. */
.degraded { color: var(--yellow); }
.node-card { display: flex; flex-direction: column; }
.head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.head-side { display: flex; align-items: flex-start; gap: 10px; }
.node-name {
  margin: 0 0 6px; color: var(--text); font-size: 13px; font-weight: 800;
  letter-spacing: 0.04em; line-height: 1; text-transform: uppercase;
}
.node-latency {
  margin: 0; font-size: 28px; font-weight: 850; line-height: 1.15;
  font-variant-numeric: tabular-nums;
}
.pair-count {
  color: var(--muted); font-size: 11px; font-weight: 750;
  letter-spacing: 0.03em; text-transform: uppercase; margin-top: 4px; white-space: nowrap;
}
/* One row per pair rather than a matrix: the landing nodes are a list, not two
   axes, and their names are long enough that a column head would truncate.
   The rows flow into as many columns as the card is wide enough for, so a relay
   with one landing node and a relay with eight both fill their card. */
/* auto-fill, not auto-fit: fitting two landing nodes to the full width of a
   wide card stretches each track to ~540px and leaves a name a third of a
   screen from its own reading. Fixed-ish tracks keep the pair legible and cost
   only a blank track at the end of the last row. */
.pairs {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 5px 18px; margin-top: 16px;
}
.pair { display: grid; grid-template-columns: minmax(0, 1fr) 118px; gap: 10px; align-items: center; }
/* The landing node's own address sits under its name: it is what the relay
   forwards to and what the client's SNI still names, so it belongs to the pair
   rather than to a tooltip. */
.pair-title { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.pair-name {
  color: var(--text); font-size: 13px; font-weight: 650;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.pair-host {
  color: var(--muted); font-size: 11px; font-weight: 600; font-variant-numeric: tabular-nums;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.cell {
  display: flex; flex-direction: column; overflow: hidden;
  height: 46px; border-radius: 10px;
  background: var(--fill); color: var(--ink);
  font-variant-numeric: tabular-nums; cursor: default;
}
.value { flex: 1; display: grid; place-items: center; font-size: 15px; font-weight: 800; line-height: 1; }
.loss {
  display: flex; align-items: center; gap: 6px;
  height: 19px; padding: 0 7px;
  background: rgba(255, 255, 255, 0.88);
}
.track {
  flex: 1; height: 4px; border-radius: 999px; overflow: hidden;
  background: rgba(15, 23, 42, 0.13);
}
.fill { display: block; height: 100%; width: var(--loss); border-radius: inherit; background: var(--loss-color); }
.pct {
  min-width: 26px; text-align: right;
  font-size: 10px; font-weight: 800; letter-spacing: -0.01em;
  color: var(--loss-ink);
}
@media (max-width: 720px) {
  .pairs { grid-template-columns: minmax(0, 1fr); }
  .pair { grid-template-columns: minmax(0, 1fr) 104px; }
  .cell { height: 44px; }
  .value { font-size: 14px; }
}
</style>
