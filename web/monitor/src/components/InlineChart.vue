<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, shallowRef, useTemplateRef, watch } from "vue";
import * as echarts from "echarts/core";

// A chart that lives inside a page rather than a modal: it renders whatever
// option it is handed and rebuilds when that option changes. The modal charts
// keep using useTrendChart, which also owns their loading and Escape handling.
const props = defineProps<{ option: Record<string, any> }>();

const host = useTemplateRef<HTMLDivElement>("host");
const chart = shallowRef<echarts.ECharts>();
let resizeHandler: (() => void) | undefined;
let resizeTimer: number | undefined;

onMounted(async () => {
  await nextTick();
  if (!host.value) return;
  chart.value = echarts.init(host.value);
  chart.value.setOption(props.option);
  resizeHandler = () => {
    if (resizeTimer) window.clearTimeout(resizeTimer);
    resizeTimer = window.setTimeout(() => {
      chart.value?.resize();
      chart.value?.setOption(props.option, true);
    }, 120);
  };
  window.addEventListener("resize", resizeHandler);
});

onUnmounted(() => {
  if (resizeHandler) window.removeEventListener("resize", resizeHandler);
  if (resizeTimer) window.clearTimeout(resizeTimer);
  chart.value?.dispose();
});

watch(
  () => props.option,
  (option) => chart.value?.setOption(option, true),
);
</script>

<template>
  <div ref="host" class="inline-chart"></div>
</template>

<style scoped>
.inline-chart { width: 100%; height: 300px; }
@media (max-width: 720px) {
  .inline-chart { height: 260px; }
}
</style>
