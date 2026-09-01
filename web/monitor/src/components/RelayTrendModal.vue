<script setup lang="ts">
import { computed, ref } from "vue";
import TrendShell from "./TrendShell.vue";
import { usePingTrend } from "../pingTrend";
import { tzOffsetMinutes } from "../timezone";
import { useTrendChart } from "../useTrendChart";
import type { PingTarget } from "../types";

// The week of relay-to-landing rounds for one relay, one line per landing node.
// The page decides which landing nodes those are, because a link the hub has
// stood down still has the rounds it recorded while it stood and is worth a line
// of its own — the relay has simply stopped adding to it.
const props = defineProps<{ nodeKey: string; nodeName: string; targets: PingTarget[] }>();
const emit = defineEmits<{ close: [] }>();

const showPeakAverage = ref(false);

// Every landing node is drawn; the chart's own legend is what turns one off,
// so a second row of chips above it would only be the same control twice.
const shownTargets = computed(() => props.targets);

function seriesName(target: PingTarget): string {
  return target.name || target.id;
}

const { error, load, spanLabel, buildOption } = usePingTrend(props.nodeKey, shownTargets, seriesName);

function close() {
  emit("close");
}

const { chartRef, loading } = useTrendChart(
  load,
  () => buildOption(chartRef.value, showPeakAverage.value),
  [tzOffsetMinutes],
  close,
  [showPeakAverage],
);
</script>

<template>
  <TrendShell
    :title="nodeName"
    :subtitle="`Landing nodes · ${spanLabel}`"
    v-model:peakAverage="showPeakAverage"
    :loading="loading"
    :error="error"
    @close="close"
  >
    <div ref="chartRef" class="chart-container"></div>
  </TrendShell>
</template>
