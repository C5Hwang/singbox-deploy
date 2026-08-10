<script setup lang="ts">
import { computed, onMounted, onUnmounted } from "vue";
import InlineChart from "./InlineChart.vue";
import { buildFrame, lineSeries, bytesAxis, SOURCE_COLORS } from "../chartOptions";
import { tzOffsetMinutes } from "../timezone";
import { formatBytes } from "../utils";
import type { IPTrafficRow } from "../types";

const props = defineProps<{ row: IPTrafficRow; location: string }>();
const emit = defineEmits<{ close: [] }>();

function close() {
  emit("close");
}

let keyHandler: ((e: KeyboardEvent) => void) | undefined;
onMounted(() => {
  keyHandler = (e) => {
    if (e.key === "Escape") close();
  };
  window.addEventListener("keydown", keyHandler);
});
onUnmounted(() => {
  if (keyHandler) window.removeEventListener("keydown", keyHandler);
});

// Counters are wiped at each quota reset, so the series never spans more than
// one cycle and a daily bucket is the natural resolution for it.
const option = computed(() => {
  void tzOffsetMinutes.value;
  const { narrow, option } = buildFrame({
    width: window.innerWidth,
    unit: "day",
    legend: ["Inbound", "Outbound", "Total"],
    tooltipUnit: "day",
    tooltipValue: (p) => formatBytes(Array.isArray(p.value) ? p.value[1] : p.value),
  });
  const series = [
    { name: "Inbound", key: "inBytes" as const },
    { name: "Outbound", key: "outBytes" as const },
    { name: "Total", key: "totalBytes" as const },
  ].map((def, i) =>
    lineSeries(
      def.name,
      SOURCE_COLORS[i % SOURCE_COLORS.length],
      props.row.daily.map((p) => [p.dayTs * 1000, p[def.key]] as [number, number]),
      { showSymbol: !narrow },
    ),
  );
  return { ...option, yAxis: bytesAxis(narrow), series };
});
</script>

<template>
  <div class="modal-backdrop" @click.self="close">
    <div class="modal-content">
      <button class="close-btn" @click="close" aria-label="Close">&times;</button>
      <div class="modal-header">
        <div>
          <h2 class="modal-title">{{ row.ip }}</h2>
          <p class="modal-subtitle">
            {{ location || "Location unavailable" }} ·
            {{ formatBytes(row.totalBytes) }} this cycle · {{ row.nodes.join(", ") }}
          </p>
        </div>
      </div>
      <div class="chart-container">
        <InlineChart :option="option" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.chart-container :deep(.inline-chart) { height: 420px; }
@media (max-width: 720px) {
  .chart-container :deep(.inline-chart) { height: 340px; }
}
</style>
