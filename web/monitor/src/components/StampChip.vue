<script setup lang="ts">
import { computed } from "vue";
import { formatAgo, formatUntil, now } from "../clock";
import { formatDateTime } from "../utils";

// A stamp is the one line on a card that is about time rather than about
// traffic: when the quota cycle turns over, or how old the reading is. Set in
// the same muted mono as the node's ID, both read as part of the ID. A chip
// gives each a shape of its own, and the sample stamp takes a colour that says
// whether the figures above it are current.
const props = defineProps<{
  kind: "reset" | "sampled";
  // The stamp, as the node reported it. Absent when the node has not said.
  at?: string;
}>();

// Nodes sample once a minute and the hub re-reads them every thirty seconds,
// so a reading older than two minutes has missed a round, and one older than
// ten has missed most of them.
const FRESH_MS = 2 * 60 * 1000;
const AGING_MS = 10 * 60 * 1000;

const stampMs = computed(() => (props.at ? new Date(props.at).getTime() : NaN));
const known = computed(() => !Number.isNaN(stampMs.value));

const label = computed(() => (props.kind === "reset" ? "reset in" : "sampled"));

const value = computed(() => {
  if (!known.value) return "NA";
  return props.kind === "reset" ? formatUntil(props.at!) : formatAgo(props.at!);
});

// The reset is a date, and a date has no health; the sample has one.
const tone = computed(() => {
  if (props.kind === "reset") return known.value ? "accent" : "none";
  if (!known.value) return "none";
  const age = now.value - stampMs.value;
  if (age <= FRESH_MS) return "fresh";
  if (age <= AGING_MS) return "aging";
  return "stale";
});

const title = computed(() => {
  if (props.kind === "reset") return known.value ? `Cycle resets ${formatDateTime(props.at!)}` : "No cycle reset reported";
  return known.value ? `Sampled ${formatDateTime(props.at!)}` : "This node has not reported a sample yet";
});
</script>

<template>
  <span class="stamp-chip" :class="tone" :title="title">
    <svg v-if="kind === 'reset'" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M13.5 8A5.5 5.5 0 0 0 4 4.4L2.5 6" />
      <path d="M2.5 2.5V6H6" />
      <path d="M2.5 8A5.5 5.5 0 0 0 12 11.6l1.5-1.6" />
      <path d="M13.5 13.5V10H10" />
    </svg>
    <svg v-else viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M1.5 8.5h3l2-5 3 9 2-4h3" />
    </svg>
    <span class="stamp-label">{{ label }}</span>
    <strong class="stamp-value">{{ value }}</strong>
  </span>
</template>
