<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref, watch } from "vue";

// rejectedAt is a stamp rather than a flag: the same wrong token typed twice has
// to announce itself twice, and a boolean that is already true cannot say
// anything the second time.
const props = defineProps<{ rejectedAt: number }>();
const emit = defineEmits<{ submit: [token: string] }>();

// How long the notice stays up. Long enough to read twice, short enough that it
// is gone before the next attempt.
const NOTICE_MS = 4500;

const token = ref("");
const input = ref<HTMLInputElement | null>(null);
const noticeVisible = ref(false);
let hideTimer: number | undefined;

onMounted(() => nextTick(() => input.value?.focus()));
onUnmounted(() => {
  if (hideTimer) window.clearTimeout(hideTimer);
});

watch(
  () => props.rejectedAt,
  (at) => {
    if (!at) return;
    if (hideTimer) window.clearTimeout(hideTimer);
    noticeVisible.value = true;
    hideTimer = window.setTimeout(() => (noticeVisible.value = false), NOTICE_MS);
  },
  { immediate: true },
);

function submit() {
  const value = token.value.trim();
  if (value) emit("submit", value);
}
</script>

<template>
  <div class="gate">
    <!-- The notice floats above the card rather than sitting inside it: an
         inline line appearing and disappearing would move the button under the
         cursor every time a token is refused. -->
    <Transition name="notice">
      <div v-if="noticeVisible" class="gate-notice" role="alert">
        <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <circle cx="12" cy="12" r="9" stroke="currentColor" stroke-width="2" />
          <path d="M12 7.5v5.5" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
          <circle cx="12" cy="16.4" r="1.15" fill="currentColor" />
        </svg>
        <span>That access token was not accepted. Check it and try again.</span>
      </div>
    </Transition>

    <form class="gate-card" @submit.prevent="submit">
      <div class="brand-logo gate-logo" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <path d="M20.5 8.5A9.5 9.5 0 1 1 15.5 3.3" />
          <path d="M16.2 10.2A5 5 0 1 1 13.8 7.3" />
          <circle cx="12" cy="12" r="1.4" fill="currentColor" stroke="none" />
          <path d="M12 12l7-7" />
        </svg>
      </div>
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

      <button class="gate-button" type="submit" :disabled="!token.trim()">Unlock</button>
      <p class="gate-foot">singbox-deploy</p>
    </form>
  </div>
</template>

<style scoped>
.gate {
  display: grid; place-items: center; min-height: var(--shell-h, 100dvh); padding: 24px;
  background:
    radial-gradient(900px 480px at 50% -10%, var(--glow-1), transparent 62%),
    radial-gradient(700px 400px at 100% 100%, var(--glow-2), transparent 58%),
    var(--bg);
}
/* The card's own surface and entrance rather than the shared .card rule: that
   one is written for a grid of dashboard cards, and its animation replays on
   anything that remounts. The gate is one card that appears once. */
.gate-card {
  display: flex; flex-direction: column; align-items: center;
  width: min(100%, 400px); padding: 34px 30px 26px; text-align: center;
  background: var(--surface-solid); border: 1px solid var(--line-strong);
  border-radius: var(--radius-lg); box-shadow: var(--shadow-hover);
  animation: gateIn 0.4s var(--ease) both;
}
@keyframes gateIn { from { opacity: 0; transform: translateY(10px); } }

.gate-notice {
  position: fixed; top: 24px; left: 50%; z-index: 50;
  display: flex; align-items: center; gap: 10px;
  max-width: min(92vw, 440px); padding: 12px 16px;
  border-radius: 14px; border: 1px solid color-mix(in srgb, var(--red) 30%, transparent);
  background: var(--surface-solid); color: var(--red);
  font-size: 13px; font-weight: 650; line-height: 1.35; text-align: left;
  box-shadow: var(--shadow-hover);
}
.gate-notice svg { width: 18px; height: 18px; flex-shrink: 0; }
/* Translate and opacity only, so the notice slides on the compositor rather
   than repainting the card behind it. */
.notice-enter-active { transition: opacity 0.24s ease, transform 0.32s var(--ease); }
.notice-leave-active { transition: opacity 0.3s ease, transform 0.3s ease; }
.notice-enter-from { opacity: 0; transform: translate(-50%, -14px); }
.notice-leave-to { opacity: 0; transform: translate(-50%, -8px); }
.notice-enter-to, .notice-leave-from { opacity: 1; transform: translate(-50%, 0); }
.gate-notice { transform: translate(-50%, 0); }

@media (prefers-reduced-motion: reduce) {
  .gate-card { animation: none; }
  .notice-enter-active, .notice-leave-active { transition: opacity 0.2s ease; }
  .notice-enter-from, .notice-leave-to { transform: translate(-50%, 0); }
}
.gate-logo { width: 52px; height: 52px; border-radius: 15px; }
.gate-logo svg { width: 28px; height: 28px; }
.gate-title { margin: 18px 0 0; font-size: 22px; font-weight: 800; letter-spacing: -0.02em; color: var(--text-strong); }
.gate-subtitle { margin: 8px 0 22px; color: var(--muted); font-size: 14px; }
.gate-input {
  width: 100%; border: 1px solid var(--line-strong); border-radius: 12px;
  padding: 12px 16px; background: var(--surface-2); color: var(--text);
  font: inherit; font-family: var(--font-mono); font-size: 15px; letter-spacing: 0.04em;
  transition: border-color var(--dur) ease;
}
.gate-input::placeholder { color: var(--faint); font-family: var(--font-sans); letter-spacing: 0; }
/* The focus ring is an outline, not an animated shadow. Returning to the tab
   re-focuses this input, and a transitioned ring makes that read as a flash of
   the whole field; an outline just appears with the caret. */
.gate-input:focus {
  outline: 3px solid var(--accent-soft);
  outline-offset: 0;
  border-color: var(--accent);
}
/* Typing the first character enables this button, and clearing the field
   disables it again. Every property that differs between the two states is
   transitioned on the same curve — the shadow used to snap from none to a wide
   glow while the opacity faded, and that mismatch under a moving element is
   what read as a flicker on every keystroke that crossed empty. */
.gate-button {
  position: relative; isolation: isolate;
  width: 100%; margin-top: 16px; border: none; border-radius: 12px; padding: 12px;
  background: linear-gradient(135deg, var(--blue), var(--cyan)); color: white;
  font: inherit; font-size: 15px; font-weight: 750; cursor: pointer;
  opacity: 1;
  transition: filter 0.18s ease, transform 0.18s ease, opacity 0.18s ease;
}
/* The glow is a pseudo-element so it fades with opacity — a compositor-friendly
   property — rather than the browser re-rasterising a box-shadow each frame. */
.gate-button::after {
  content: ""; position: absolute; inset: 0; z-index: -1; border-radius: inherit;
  box-shadow: 0 12px 28px color-mix(in srgb, var(--blue) 35%, transparent);
  opacity: 1; transition: opacity 0.18s ease;
}
.gate-button:hover:not(:disabled) { filter: brightness(1.06); transform: translateY(-1px); }
.gate-button:disabled { opacity: 0.5; cursor: not-allowed; }
.gate-button:disabled::after { opacity: 0; }
.gate-foot {
  margin: 18px 0 0; color: var(--faint); font-family: var(--font-mono);
  font-size: 10.5px; letter-spacing: 0.1em; text-transform: uppercase;
}
@media (prefers-reduced-motion: reduce) {
  .gate-button, .gate-button::after { transition: none; }
}
</style>
