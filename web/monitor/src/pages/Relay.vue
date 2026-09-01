<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import RelayTrendModal from "../components/RelayTrendModal.vue";
import { fetchLatency } from "../api";
import { LATENCY_DEAD, LATENCY_MISSING, LATENCY_STEPS, LOSS_CLEAR, LOSS_CRITICAL, LOSS_WARNING } from "../latencyScale";
import {
  RELAY_TARGET_KIND,
  relayTargetID,
  relayTargets,
  type LatencySnapshot,
  type PingLatestPoint,
  type PingTarget,
  type RelayLink,
  type Summary,
} from "../types";

// One card per relay: the pairs it forwards, with the latency it measures to
// each landing node. The probe runs on the relay itself, so this is the route
// the relayed traffic actually takes — a client's own latency is that plus its
// hop to the relay.
//
// The rows come from the hub's registry rather than from the probes, because a
// relay only probes what it currently forwards: the hub stands a link down as
// soon as either end runs out of traffic, and the landing node would otherwise
// leave the page entirely at the moment an operator wants to know where it went.
const props = defineProps<{ summary: Summary | null }>();

const sources = computed(() => {
  const list = props.summary?.sources ?? [];
  if (list.length > 0) return list;
  return props.summary ? [{ id: "local", name: "Local Server" }] : [];
});

function sourceKey(source: { id?: string; name?: string }): string {
  return source.id || source.name || "";
}

// The hub's registry names which nodes relay, so those are the only ones asked.
// Asking the whole fleet and discarding the answers with no relay probes would
// let any unrelated node — including one that is powered off, which can only
// fail by timing out — set the pace of a page it has nothing to do with. A
// deployment that sends no list has no registry to ask, and falls back to
// asking every node.
const relaySources = computed<{ id?: string; name?: string }[]>(() => {
  const ids = props.summary?.relayNodes ?? [];
  if (ids.length === 0) return sources.value;
  const byKey = new Map(sources.value.map((source) => [sourceKey(source), source]));
  return ids.map((id) => byKey.get(id)).filter((source) => source !== undefined);
});

interface RelayNode {
  key: string;
  name: string;
  snapshot: LatencySnapshot;
}

// One entry per node that has answered: the relay it describes, or null for a
// node that answered without relay probes or could not be read at all. A node
// that is missing from the map has not answered yet.
const answers = ref(new Map<string, RelayNode | null>());
const openRelay = ref<RelayNode | null>(null);
let round = 0;

const relays = computed(() =>
  relaySources.value
    .map((source) => answers.value.get(sourceKey(source)))
    .filter((node): node is RelayNode => !!node),
);
const pending = computed(() => relaySources.value.some((source) => !answers.value.has(sourceKey(source))));

// Each relay's card appears the moment that relay answers. A node that cannot be
// reached is already reported on the latency page, so here it simply never gets
// a card, and it does not hold up the ones that did answer.
function load() {
  const targets = relaySources.value;
  if (targets.length === 0) return;
  const current = ++round;
  for (const source of targets) {
    const key = sourceKey(source);
    const name = source.name ?? key;
    fetchLatency(key)
      // A relay that reports no probe still gets a card when the hub says one of
      // its links is stood down, which is exactly the case that has no probe to
      // report. Without a stood-down link the old reading stands: a relay with
      // nothing to say is a relay whose monitor is off, and it gets no card.
      .then((snapshot) =>
        relayTargets(snapshot.targets).length === 0 && !linksFor(key).some((link) => !link.forwarding)
          ? null
          : { key, name, snapshot },
      )
      .catch(() => null)
      .then((node) => {
        if (current !== round) return;
        answers.value.set(key, node);
        if (openRelay.value?.key === key) openRelay.value = node;
      });
  }
}

watch(() => relaySources.value.map(sourceKey).join(","), load, { immediate: true });

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
  ms: number | null;
  loss: number | null;
  text: string;
  lossText: string;
  style: Record<string, string>;
  title: string;
  // probed is false for a link nothing is measuring: a relay probes only what it
  // forwards, so a stood-down link usually has no reading at all.
  probed: boolean;
  // stoodDown marks a link the hub is not forwarding right now, whether or not
  // the relay is still measuring it — a relay that is itself out of quota keeps
  // probing the landing nodes whose rules it has withdrawn.
  stoodDown: boolean;
  reason: string;
}

// The links the hub says this relay carries. A dashboard served without a
// registry to ask — a spoke's own — has none, and falls back to the probes.
function linksFor(key: string): RelayLink[] {
  return (props.summary?.relayTopology ?? []).filter((link) => link.relay === key);
}

// One row per link the hub reports, in registry order, then whatever the relay
// probed that the hub did not name: every row on a dashboard sent no topology,
// and a probe left over from a link the registry has since dropped.
function pairs(node: RelayNode): Pair[] {
  const probes = new Map(relayTargets(node.snapshot.targets).map((target) => [target.id, target]));
  const rows: Pair[] = [];
  for (const link of linksFor(node.key)) {
    const id = relayTargetID(link.landing);
    const target = probes.get(id);
    probes.delete(id);
    rows.push(pair(node, id, target?.name || link.name || link.landing, target, link));
  }
  for (const target of probes.values()) rows.push(pair(node, target.id, target.name || target.id, target));
  return rows;
}

// A link with no probe is left without a reading rather than given its last one:
// the newest round is looked up in a week of samples, so the measurement a
// withdrawn link was carrying when it stopped would otherwise be printed days
// later as though it were current.
function pair(node: RelayNode, id: string, name: string, target: PingTarget | undefined, link?: RelayLink): Pair {
  const latest: PingLatestPoint | undefined = target ? node.snapshot.latest.find((p) => p.target === id) : undefined;
  const ms = latest?.avgMs ?? null;
  const loss = latest ? latest.lossPct : null;
  const tone = ms === null ? (latest ? LATENCY_DEAD : LATENCY_MISSING) : LATENCY_STEPS.find((s) => ms < s.limit)!;
  const text = ms === null ? (latest ? "out" : "—") : ms >= 100 ? String(Math.round(ms)) : ms.toFixed(1);
  const stoodDown = link ? !link.forwarding : false;
  const reason = stoodDown ? link?.reason || "the hub is not forwarding this link" : "";
  const reading = `${ms === null ? "no answer" : `${text} ms`}, ${loss === null ? "no data" : `${Math.round(loss)}% loss`}`;
  return {
    id,
    name,
    ms,
    loss,
    text,
    probed: target !== undefined,
    stoodDown,
    reason,
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
    title: stoodDown
      ? `${node.name} → ${name} — stood down: ${reason}${target ? ` (still measured: ${reading})` : ""}`
      : `${node.name} → ${name} — ${reading}`,
  };
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

// The lines the trend chart draws: every probe the relay reported, plus a stub
// for each stood-down link so the rounds it recorded while it stood are drawn
// too. They are in the same week the chart reads; only the relay has stopped
// adding to them, which is exactly what the gap at the end says.
function trendTargets(node: RelayNode): PingTarget[] {
  const probes = relayTargets(node.snapshot.targets);
  const probed = new Set(probes.map((target) => target.id));
  const stood = linksFor(node.key)
    .filter((link) => !link.forwarding && !probed.has(relayTargetID(link.landing)))
    .map((link) => ({
      id: relayTargetID(link.landing),
      kind: RELAY_TARGET_KIND,
      name: link.name || link.landing,
      address: "",
    }));
  return [...probes, ...stood];
}

// The card's headline is the relay's median reachable landing node, so one
// unreachable landing node cannot speak for the whole relay. A link nothing is
// measuring is left out of every headline figure below: a stood-down landing
// node is not a route that is down, it is a route that is not being used.
function measured(node: RelayNode): Pair[] {
  return pairs(node).filter((p) => p.probed);
}

function medianLatency(node: RelayNode): number | null {
  return median(measured(node).map((p) => p.ms).filter((v): v is number => v !== null));
}

function pairCount(node: RelayNode): string {
  const count = pairs(node).length;
  return `${count} landing node${count === 1 ? "" : "s"}`;
}

// One dot for the whole relay: green when every landing answered clean, amber
// when something is losing packets, red when a route is down.
function statusTone(node: RelayNode): string {
  const list = measured(node);
  if (list.length === 0) return "gray";
  if (list.some((p) => p.ms === null || (p.loss ?? 0) >= 100)) return "danger";
  if (list.some((p) => (p.loss ?? 0) > 0)) return "warn";
  return "ok";
}

function statusLabel(node: RelayNode): string {
  const list = measured(node);
  const stood = pairs(node).filter((p) => p.stoodDown).length;
  const parts = [
    list.length > 0
      ? `${list.filter((p) => p.ms !== null).length} of ${list.length} landing nodes answering`
      : "no landing node is being measured",
  ];
  if (stood > 0) parts.push(`${stood} stood down`);
  return parts.join(", ");
}

</script>

<template>
  <p v-if="relays.length === 0 && pending" class="no-data">Loading relay latency data...</p>
  <!-- The navigation only offers this page once something is relayed, so an
       empty one means the relays exist but none of them has reported a round —
       most often because the relay's own monitor is switched off. -->
  <p v-else-if="relays.length === 0" class="no-data">
    No relay is reporting yet. A relay measures the route to each landing node it forwards to, and its readings appear
    here once its monitor has run a round.
  </p>

  <template v-if="relays.length">
    <div class="scale">
      <span>faster</span>
      <i v-for="step in LATENCY_STEPS" :key="step.fill" :style="{ background: step.fill }"></i>
      <span>slower</span>
      <em class="scale-loss"><i :style="{ background: LOSS_WARNING }"></i>packet loss</em>
    </div>

    <section class="grid" aria-label="relay to landing latency">
      <article
        v-for="node in relays"
        :key="node.key"
        class="card span-6 node-card clickable"
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
            <span class="pair-name" role="rowheader">
              <span class="name-text" :title="pair.name">{{ pair.name }}</span>
              <!-- The row stays when the hub stops forwarding the link, and says
                   so where the reading would have gone. -->
              <span v-if="pair.stoodDown" class="stood" :title="pair.reason">stood down</span>
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
    :targets="trendTargets(openRelay)"
    @close="openRelay = null"
  />
</template>

<style scoped>
.scale {
  display: flex; align-items: center; gap: 6px;
  margin: 0 2px 12px; color: var(--muted); font-size: 11px; font-weight: 700;
  letter-spacing: 0.03em; text-transform: uppercase;
}
.scale i { width: 26px; height: 7px; border-radius: 2px; }
.scale-loss { display: inline-flex; align-items: center; gap: 6px; margin-left: 14px; font-style: normal; }
.scale-loss i { width: 18px; height: 4px; border-radius: 999px; }
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
/* One row per pair, one column of readings: the landing nodes are a list, not
   two axes, and stacking the cells in a single column lets the eye run down
   them without hopping between rows of different widths. */
.pairs { display: flex; flex-direction: column; gap: 5px; margin-top: 16px; }
.pair { display: grid; grid-template-columns: minmax(0, 1fr) 118px; gap: 10px; align-items: center; }
.pair-name {
  display: flex; align-items: center; gap: 7px; min-width: 0;
  color: var(--text); font-size: 13px; font-weight: 650;
}
.name-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.stood {
  flex: none; padding: 2px 6px; border-radius: 999px;
  background: rgba(15, 23, 42, 0.06); color: var(--muted);
  font-size: 10px; font-weight: 800; letter-spacing: 0.03em; text-transform: uppercase;
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
  .pair { grid-template-columns: minmax(0, 1fr) 104px; }
  .cell { height: 44px; }
  .value { font-size: 14px; }
}
</style>
