<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import {
  browserOffsetMinutes,
  clearTzOverride,
  gmtLabel,
  setTzOffset,
  shiftToTz,
  tzOffsetMinutes,
  tzOptions,
  tzOverridden,
} from "../timezone";

const open = ref(false);
const now = ref(new Date());
const rootRef = ref<HTMLElement>();
let clockTimer: number | undefined;

const options = tzOptions();
const detectedLabel = gmtLabel(browserOffsetMinutes());

const clockLabel = computed(() => {
  const time = shiftToTz(now.value).toLocaleTimeString("en-US", { hour12: false, timeZone: "UTC" });
  return `${time} ${gmtLabel(tzOffsetMinutes.value)}`;
});

function choose(minutes: number) {
  setTzOffset(minutes);
  open.value = false;
}

function chooseAuto() {
  clearTzOverride();
  open.value = false;
}

function onDocumentClick(e: MouseEvent) {
  if (open.value && rootRef.value && !rootRef.value.contains(e.target as Node)) {
    open.value = false;
  }
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === "Escape") open.value = false;
}

onMounted(() => {
  clockTimer = window.setInterval(() => {
    now.value = new Date();
  }, 1000);
  document.addEventListener("click", onDocumentClick);
  document.addEventListener("keydown", onKeydown);
});
onUnmounted(() => {
  if (clockTimer) window.clearInterval(clockTimer);
  document.removeEventListener("click", onDocumentClick);
  document.removeEventListener("keydown", onKeydown);
});
</script>

<template>
  <div ref="rootRef" class="menu-picker">
    <button
      class="chip menu-chip"
      :class="{ open }"
      aria-haspopup="listbox"
      :aria-expanded="open"
      aria-label="Display timezone"
      @click="open = !open"
    >
      {{ clockLabel }}
      <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <path d="M4 6l4 4 4-4" />
      </svg>
    </button>

    <div v-if="open" class="menu-pop" role="listbox" aria-label="Display timezone">
      <div class="menu-pop-head">Display Timezone</div>
      <div class="menu-pop-list">
        <button
          class="menu-option"
          role="option"
          :aria-selected="!tzOverridden"
          :class="{ active: !tzOverridden }"
          @click="chooseAuto"
        >
          Auto
          <span class="menu-note">Browser · {{ detectedLabel }}</span>
        </button>
        <button
          v-for="opt in options"
          :key="opt.minutes"
          class="menu-option"
          role="option"
          :aria-selected="tzOverridden && opt.minutes === tzOffsetMinutes"
          :class="{ active: tzOverridden && opt.minutes === tzOffsetMinutes }"
          @click="choose(opt.minutes)"
        >
          {{ opt.label }}
        </button>
      </div>
    </div>
  </div>
</template>
