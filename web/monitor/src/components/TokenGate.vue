<script setup lang="ts">
import { nextTick, onMounted, ref } from "vue";

defineProps<{ rejected: boolean }>();
const emit = defineEmits<{ submit: [token: string] }>();

const token = ref("");
const input = ref<HTMLInputElement | null>(null);

onMounted(() => nextTick(() => input.value?.focus()));

function submit() {
  const value = token.value.trim();
  if (value) emit("submit", value);
}
</script>

<template>
  <div class="gate">
    <form class="gate-card" @submit.prevent="submit">
      <div class="brand-logo gate-logo">M</div>
      <h1 class="gate-title">Monitor</h1>
      <p class="gate-subtitle">Enter the access token to view this dashboard.</p>

      <input
        ref="input"
        v-model="token"
        class="gate-input"
        type="password"
        autocomplete="current-password"
        spellcheck="false"
        placeholder="Access token"
        aria-label="Access token"
      />
      <p v-if="rejected" class="gate-error">That token was rejected. Check it on the hub's Status screen.</p>

      <button class="gate-button" type="submit" :disabled="!token.trim()">Unlock</button>
    </form>
  </div>
</template>

<style scoped>
.gate { display: grid; place-items: center; min-height: 100vh; padding: 24px; }
/* The card's own surface and entrance rather than the shared .card rule: that
   one is written for a grid of dashboard cards, and its animation replays on
   anything that remounts. The gate is one card that appears once. */
.gate-card {
  display: flex; flex-direction: column; align-items: center;
  width: min(100%, 400px); padding: 34px 30px 30px; text-align: center;
  background: var(--card); border: 1px solid rgba(231, 236, 244, 0.94);
  border-radius: var(--radius-xl); box-shadow: var(--shadow);
  animation: gateIn 0.4s cubic-bezier(0.2, 0.8, 0.2, 1) both;
}
@keyframes gateIn { from { opacity: 0; transform: translateY(10px); } }
/* The rejection line is the only part that changes while the gate is open, so
   it fades in on its own instead of the card re-announcing itself. */
.gate-error { animation: gateErrorIn 0.22s ease both; }
@keyframes gateErrorIn { from { opacity: 0; transform: translateY(-3px); } }
@media (prefers-reduced-motion: reduce) {
  .gate-card, .gate-error { animation: none; }
}
.gate-logo { width: 52px; height: 52px; font-size: 20px; }
.gate-title { margin: 18px 0 0; font-size: 24px; letter-spacing: -0.02em; }
.gate-subtitle { margin: 8px 0 22px; color: var(--muted); font-size: 14px; }
.gate-input {
  width: 100%; border: 1px solid var(--line); border-radius: 14px;
  padding: 13px 16px; background: white; color: var(--text);
  font: inherit; font-size: 15px; letter-spacing: 0.04em;
  transition: border-color 0.15s;
}
/* The focus ring is an outline, not an animated shadow. Returning to the tab
   re-focuses this input, and a transitioned ring makes that read as a flash of
   the whole field; an outline just appears with the caret. */
.gate-input:focus {
  outline: 3px solid color-mix(in srgb, var(--blue), transparent 82%);
  outline-offset: 0;
  border-color: var(--blue);
}
.gate-error { margin: 12px 0 0; color: var(--red); font-size: 13px; font-weight: 650; }
/* Typing the first character enables this button, and clearing the field
   disables it again. Every property that differs between the two states is
   transitioned on the same curve — the shadow used to snap from none to a wide
   glow while the opacity faded, and that mismatch under a moving element is
   what read as a flicker on every keystroke that crossed empty. */
.gate-button {
  position: relative; isolation: isolate;
  width: 100%; margin-top: 18px; border: none; border-radius: 14px; padding: 13px;
  background: linear-gradient(135deg, var(--blue), var(--cyan)); color: white;
  font: inherit; font-size: 15px; font-weight: 750; cursor: pointer;
  opacity: 1;
  transition: filter 0.18s ease, transform 0.18s ease, opacity 0.18s ease;
}
/* The glow is a pseudo-element so it fades with opacity — a compositor-friendly
   property — rather than the browser re-rasterising a box-shadow each frame. */
.gate-button::after {
  content: ""; position: absolute; inset: 0; z-index: -1; border-radius: inherit;
  box-shadow: 0 12px 28px rgba(37, 99, 235, 0.28);
  opacity: 1; transition: opacity 0.18s ease;
}
.gate-button:hover:not(:disabled) { filter: brightness(1.06); transform: translateY(-1px); }
.gate-button:disabled { opacity: 0.5; cursor: not-allowed; }
.gate-button:disabled::after { opacity: 0; }
@media (prefers-reduced-motion: reduce) {
  .gate-button, .gate-button::after { transition: none; }
}
</style>
