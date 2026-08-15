<script setup lang="ts">
import { computed } from "vue";
import {
  LATENCY_DEAD,
  LATENCY_MISSING,
  LATENCY_STEPS,
  LOSS_CLEAR,
  LOSS_CRITICAL,
  LOSS_WARNING,
} from "../latencyScale";
import { carrierTargets, type LatencySnapshot, type PingLatestPoint, type PingTarget } from "../types";

// One carrier per row, one city per column: the two comparisons an operator
// actually makes — this carrier across the country, this city across carriers —
// are a row and a column rather than a search through nine text lines.
const props = defineProps<{ snapshot: LatencySnapshot }>();

// Only the fixed probe list belongs in this grid. A relay's probes have no
// carrier and no city, and would each add an empty row and column.
const targets = computed(() => carrierTargets(props.snapshot.targets));
const carriers = computed(() => unique((t) => t.carrier ?? ""));
const cities = computed(() => unique((t) => t.city ?? ""));

function unique(pick: (t: PingTarget) => string): string[] {
  const seen: string[] = [];
  for (const target of targets.value) {
    const value = pick(target);
    if (!seen.includes(value)) seen.push(value);
  }
  return seen;
}

function latestAt(carrier: string, city: string): PingLatestPoint | undefined {
  const target = targets.value.find((t) => t.carrier === carrier && t.city === city);
  return target ? props.snapshot.latest.find((p) => p.target === target.id) : undefined;
}

interface Cell {
  key: string;
  text: string;
  lossText: string;
  style: Record<string, string>;
  title: string;
}

function cell(carrier: string, city: string): Cell {
  const latest = latestAt(carrier, city);
  const ms = latest?.avgMs ?? null;
  const loss = latest ? latest.lossPct : null;
  const tone = ms === null ? (latest ? LATENCY_DEAD : LATENCY_MISSING) : LATENCY_STEPS.find((b) => ms < b.limit)!;
  const text = ms === null ? (latest ? "out" : "—") : ms >= 100 ? String(Math.round(ms)) : ms.toFixed(1);
  return {
    key: `${carrier}-${city}`,
    text,
    // Every cell prints its loss, including the zero. A figure that only appears
    // when something is wrong makes its absence ambiguous — nothing lost and
    // nothing measured look the same — and leaves the strip changing shape from
    // cell to cell. A probe that never ran has no figure to print, so that one
    // says so.
    lossText: loss === null ? "—" : `${Math.round(loss)}%`,
    style: {
      "--fill": tone.fill,
      "--ink": tone.ink,
      "--loss": `${Math.max(0, Math.min(100, loss ?? 0))}%`,
      "--loss-color": (loss ?? 0) >= 50 ? LOSS_CRITICAL : LOSS_WARNING,
      "--loss-ink": loss ? ((loss >= 50 ? LOSS_CRITICAL : LOSS_WARNING) as string) : LOSS_CLEAR,
    },
    title: `${carrier} · ${city} — ${ms === null ? "no answer" : `${text} ms`}, ${
      loss === null ? "no data" : `${Math.round(loss)}% loss`
    }`,
  };
}

// Carrier names are long enough to crowd a half-width card; the matrix only
// needs enough to tell three of them apart.
function shortCarrier(name: string): string {
  return name.replace(/^China\s+/, "");
}
</script>

<template>
  <div class="matrix" role="table" aria-label="Latency by carrier and city, milliseconds">
    <div class="row head" role="row">
      <span class="corner" role="columnheader"></span>
      <span v-for="city in cities" :key="city" class="col-head" role="columnheader">{{ city }}</span>
    </div>
    <div v-for="carrier in carriers" :key="carrier" class="row" role="row">
      <span class="row-head" role="rowheader">{{ shortCarrier(carrier) }}</span>
      <span
        v-for="city in cities"
        :key="cell(carrier, city).key"
        class="cell"
        :style="cell(carrier, city).style"
        :title="cell(carrier, city).title"
        role="cell"
      >
        <span class="value">{{ cell(carrier, city).text }}</span>
        <!-- The strip is a plate of its own rather than a bar drawn onto the
             fill: a translucent wash took the colour of whichever step it sat
             on and stopped reading as a separate thing, which is how a bar on
             the red cell became invisible. -->
        <span class="loss">
          <i class="track" aria-hidden="true"><i class="fill"></i></i>
          <span class="pct">{{ cell(carrier, city).lossText }}</span>
        </span>
      </span>
    </div>
  </div>
</template>

<style scoped>
.matrix { display: flex; flex-direction: column; gap: 5px; margin-top: 16px; }
.row { display: grid; grid-template-columns: 62px repeat(3, minmax(0, 1fr)); gap: 5px; align-items: center; }
.col-head, .row-head {
  color: var(--muted); font-size: 11px; font-weight: 750;
  letter-spacing: 0.03em; text-transform: uppercase;
}
.col-head { text-align: center; padding-bottom: 2px; }
.row-head { text-align: right; padding-right: 2px; }
.cell {
  display: flex; flex-direction: column; overflow: hidden;
  height: 56px; border-radius: 10px;
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
/* Width is the loss, so a clean probe is an empty track rather than a missing
   one: the element keeps its shape and only its fill reports anything. */
.fill { display: block; height: 100%; width: var(--loss); border-radius: inherit; background: var(--loss-color); }
.pct {
  min-width: 26px; text-align: right;
  font-size: 10px; font-weight: 800; letter-spacing: -0.01em;
  color: var(--loss-ink);
}
@media (max-width: 720px) {
  .row { grid-template-columns: 52px repeat(3, minmax(0, 1fr)); }
  .cell { height: 52px; }
  .value { font-size: 14px; }
}
</style>
