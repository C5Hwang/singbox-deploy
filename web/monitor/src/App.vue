<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watchEffect } from "vue";
import SidebarNav from "./components/SidebarNav.vue";
import TimezonePicker from "./components/TimezonePicker.vue";
import TokenGate from "./components/TokenGate.vue";
import Latency from "./pages/Latency.vue";
import NetworkTraffic from "./pages/NetworkTraffic.vue";
import Relay from "./pages/Relay.vue";
import Resources from "./pages/Resources.vue";
import TopIPs from "./pages/TopIPs.vue";
import { fetchSummary, hasStoredAccessToken, onUnauthorized, setAccessToken, UnauthorizedError } from "./api";
import { formatAgo } from "./clock";
import { theme, toggleTheme } from "./theme";
import { isLimited } from "./utils";
import type { Summary, Tab } from "./types";

// Every page, in sidebar order: its title, the short word the phone tab strip
// uses for it, and the one line under the title that says what it reports.
const PAGES: { key: Tab; title: string; short: string; blurb: string }[] = [
  { key: "traffic", title: "Network Traffic", short: "Traffic", blurb: "Cycle usage against each node's quota" },
  { key: "resources", title: "Resources", short: "Resources", blurb: "CPU, memory, disk and IO on every node" },
  { key: "topips", title: "Clients", short: "Clients", blurb: "Client addresses ranked by traffic" },
  { key: "latency", title: "Latency", short: "Latency", blurb: "Round-trip time to Chinese carriers, by city" },
  { key: "relay", title: "Relay", short: "Relay", blurb: "Relay to landing node routes and their latency" },
];

const PAGE_COMPONENTS = {
  traffic: NetworkTraffic,
  resources: Resources,
  topips: TopIPs,
  latency: Latency,
  relay: Relay,
} as const;

const activeTab = ref<Tab>("traffic");
const summary = ref<Summary | null>(null);
const error = ref<string>("");
// When the last summary landed, for the live chip in the top bar.
const updatedAt = ref(0);

// Three states, not a boolean: until the first answer comes back the dashboard
// does not know which shell it is. Starting on "unlocked" would paint the whole
// dashboard chrome — sidebar, cards, their entrance animations — and then
// replace it with the gate the moment the 401 landed, a flash of the wrong
// interface on every single load.
type Shell = "checking" | "locked" | "ready";
const shell = ref<Shell>("checking");
// A token that was already stored and then refused is a stale one, which is
// worth saying; a first visit to a gated dashboard is not an error. The stamp
// is what the gate announces on — the same wrong token typed twice has to be
// two notices, and a boolean that is already true cannot express the second.
const tokenRejectedAt = ref(0);
let loadTimer: number | undefined;

const POLL_MS = 10000;

function startPolling() {
  if (loadTimer !== undefined) return;
  loadTimer = window.setInterval(load, POLL_MS);
}

// Nothing useful can come of polling while the gate is up: every request is
// refused, and each refusal wrote the same two values back over themselves six
// times a minute. The loop resumes the moment a token is accepted.
function stopPolling() {
  if (loadTimer === undefined) return;
  window.clearInterval(loadTimer);
  loadTimer = undefined;
}

// Any view can be the one whose request is refused, so the lock is raised from
// the API layer rather than from this component's own load. The writes are
// guarded: re-rendering the gate to the same state is what a viewer sees as a
// blink.
onUnauthorized(() => {
  if (hasStoredAccessToken()) tokenRejectedAt.value = Date.now();
  if (shell.value !== "locked") shell.value = "locked";
  stopPolling();
});

async function load() {
  try {
    const res = await fetchSummary();
    summary.value = res;
    error.value = "";
    updatedAt.value = Date.now();
    if (tokenRejectedAt.value) tokenRejectedAt.value = 0;
    if (shell.value !== "ready") shell.value = "ready";
    startPolling();
  } catch (e) {
    if (e instanceof UnauthorizedError) return;
    error.value = e instanceof Error ? e.message : String(e);
    // A transport failure is not an authorization failure: an open dashboard
    // stays open and says so rather than demanding a token it does not want.
    if (shell.value === "checking") shell.value = "ready";
  }
}

function unlock(token: string) {
  setAccessToken(token);
  load();
}

const sources = computed(() => summary.value?.sources ?? []);
const sourceCount = computed(() => sources.value.length);
// A node is online for the fleet count when it still has quota to serve with;
// one that has run out is up but not serving, which is the state the traffic
// page calls limited.
const onlineCount = computed(() => sources.value.filter((s) => !isLimited(s)).length);

// The relay page is offered only once something is actually relayed. Until the
// first summary lands the count is unknown, and an entry that appears a moment
// after the page settles reads as a glitch — so it stays hidden until asked for.
const showRelay = computed(() => (summary.value?.relayLinks ?? 0) > 0);

// A relay link removed while the page is open would leave the dashboard on a
// tab its navigation no longer offers, with no way back to it.
watchEffect(() => {
  if (!showRelay.value && activeTab.value === "relay") activeTab.value = "traffic";
});

const tabs = computed(() => PAGES.filter((p) => p.key !== "relay" || showRelay.value));
const page = computed(() => PAGES.find((p) => p.key === activeTab.value)!);
const pageComponent = computed(() => PAGE_COMPONENTS[activeTab.value]);
// Only the two overview pages take the error: the rest read the summary alone,
// and handing them a prop they do not declare would land it on their markup.
const pageProps = computed(() =>
  activeTab.value === "traffic" || activeTab.value === "resources"
    ? { summary: summary.value, error: error.value }
    : { summary: summary.value },
);

const subtitle = computed(() => {
  if (error.value) return `Failed to load data: ${error.value}`;
  return page.value.blurb;
});

const liveLabel = computed(() => (updatedAt.value ? formatAgo(updatedAt.value) : "waiting"));
const liveTitle = computed(() =>
  error.value
    ? "The last poll failed; figures are from the last answer that arrived."
    : `Summary polled every ${POLL_MS / 1000}s`,
);

onMounted(load);
watchEffect(() => {
  document.title = page.value.title;
});
onUnmounted(stopPolling);
</script>

<template>
  <div v-if="shell === 'checking'" class="boot" aria-busy="true"></div>

  <TokenGate v-else-if="shell === 'locked'" :rejectedAt="tokenRejectedAt" @submit="unlock" />

  <div v-else class="app">
    <SidebarNav
      v-model:activeTab="activeTab"
      :sourceCount="sourceCount"
      :onlineCount="onlineCount"
      :showRelay="showRelay"
    />

    <main class="main">
      <header class="topbar">
        <div class="title">
          <h1 class="page-title">{{ page.title }}</h1>
          <p class="page-sub" :class="{ error: !!error }">{{ subtitle }}</p>
        </div>
        <div class="toolbar">
          <span class="chip live-chip" :class="{ stale: !!error }" :title="liveTitle">
            <span class="dot"></span>
            <span class="live-label">{{ error ? "Stale" : "Live" }}</span>
            <span class="live-ago">{{ liveLabel }}</span>
          </span>
          <TimezonePicker />
          <button
            class="chip icon-chip"
            type="button"
            :aria-label="theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'"
            :title="theme === 'dark' ? 'Light theme' : 'Dark theme'"
            @click="toggleTheme"
          >
            <svg v-if="theme === 'dark'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <circle cx="12" cy="12" r="4" />
              <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
            </svg>
            <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />
            </svg>
          </button>
        </div>
      </header>

      <!-- The phone's navigation. It is a direct child of main rather than of
           the header so it can stick to the top on its own while the title
           scrolls away above it. -->
      <nav class="tabbar" aria-label="Pages">
        <button
          v-for="tab in tabs"
          :key="tab.key"
          type="button"
          :class="{ active: activeTab === tab.key }"
          :aria-current="activeTab === tab.key ? 'page' : undefined"
          @click="activeTab = tab.key"
        >
          {{ tab.short }}
        </button>
      </nav>

      <Transition name="page" mode="out-in">
        <div :key="activeTab" class="page">
          <component :is="pageComponent" v-bind="pageProps" />
        </div>
      </Transition>

      <p class="footer-note">{{ error ? "Some data is unavailable. Refresh again later." : "" }}</p>
    </main>
  </div>
</template>

<style>
/* ── Tokens ─────────────────────────────────────────────────────
   Light is the base; dark redefines the same names. Everything below is
   written against the names, so the two themes are one stylesheet. */
:root {
  color-scheme: light;
  --bg: #f3f5fa;
  --glow-1: rgba(37, 99, 235, 0.1);
  --glow-2: rgba(8, 145, 178, 0.08);
  --grid-dot: rgba(23, 32, 51, 0.07);
  --surface: rgba(255, 255, 255, 0.86);
  --surface-2: #f6f8fc;
  --surface-solid: #ffffff;
  --text: #1a2233;
  --text-strong: #0b1220;
  --muted: #66738a;
  --faint: #97a3b6;
  --line: #e4e9f2;
  --line-strong: #cbd5e3;
  --blue: #2563eb;
  --cyan: #0891b2;
  --green: #16a34a;
  --yellow: #d97706;
  --red: #dc2626;
  --orange: #ea580c;
  --track: #e9eef6;
  --hover: rgba(23, 32, 51, 0.04);
  --shadow: 0 1px 2px rgba(18, 32, 64, 0.04), 0 10px 30px rgba(18, 32, 64, 0.06);
  --shadow-hover: 0 2px 4px rgba(18, 32, 64, 0.05), 0 18px 40px rgba(18, 32, 64, 0.1);
  --sidebar-bg: rgba(255, 255, 255, 0.72);
  --chart-text: #66738a;
  --chart-axis: #e4e9f2;
  --chart-grid: #edf1f7;
  --chart-tooltip-bg: rgba(255, 255, 255, 0.97);
  --chart-tooltip-border: #e4e9f2;
  --chart-tooltip-text: #1a2233;
  --chart-zoom-bg: #eef2f8;
  /* The latency ramp: a fill for a cell and an ink to print on it, then a
     darker grade of the same hue for a latency written as text. */
  --lat-1-fill: #4ade80; --lat-1-ink: #0b3a1e; --lat-1-text: #15803d;
  --lat-2-fill: #fbbf24; --lat-2-ink: #3d2a00; --lat-2-text: #a16207;
  --lat-3-fill: #fb7a3c; --lat-3-ink: #4a1a02; --lat-3-text: #c2410c;
  --lat-4-fill: #f2544f; --lat-4-ink: #330807; --lat-4-text: #b91c1c;
  --lat-dead-fill: #dc2626; --lat-dead-ink: #ffffff; --lat-dead-text: #b91c1c;
  --lat-none-fill: #eef2f8; --lat-none-ink: #98a2b3; --lat-none-text: #98a2b3;
  --lat-strip: rgba(255, 255, 255, 0.88);
  --lat-strip-track: rgba(15, 23, 42, 0.13);
  --loss-warning: #b45309;
  --loss-critical: #7f1d1d;
  --loss-clear: #5b6b84;

  --font-sans: Inter, "SF Pro Text", -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Noto Sans SC",
    "Microsoft YaHei", system-ui, sans-serif;
  --font-mono: "JetBrains Mono", "SF Mono", "Cascadia Mono", Menlo, Consolas, "Roboto Mono", ui-monospace, monospace;
  --radius: 14px;
  --radius-sm: 10px;
  --radius-lg: 18px;
  --ease: cubic-bezier(0.2, 0.8, 0.2, 1);
  --dur: 180ms;
  --sidebar-w: 240px;
  --rail-w: 68px;
}

@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    color-scheme: dark;
    --bg: #0a0e17;
    --glow-1: rgba(59, 130, 246, 0.16);
    --glow-2: rgba(34, 211, 238, 0.1);
    --grid-dot: rgba(148, 163, 184, 0.09);
    --surface: rgba(17, 23, 36, 0.72);
    --surface-2: rgba(30, 38, 56, 0.55);
    --surface-solid: #111724;
    --text: #e6ebf5;
    --text-strong: #f8fafc;
    --muted: #8a96ab;
    --faint: #5b6a82;
    --line: rgba(148, 163, 184, 0.14);
    --line-strong: rgba(148, 163, 184, 0.28);
    --blue: #3b82f6;
    --cyan: #22d3ee;
    --green: #34d399;
    --yellow: #fbbf24;
    --red: #f87171;
    --orange: #fb923c;
    --track: rgba(148, 163, 184, 0.14);
    --hover: rgba(148, 163, 184, 0.07);
    --shadow: inset 0 1px 0 rgba(255, 255, 255, 0.03), 0 12px 32px rgba(0, 0, 0, 0.35);
    --shadow-hover: inset 0 1px 0 rgba(255, 255, 255, 0.05), 0 18px 44px rgba(0, 0, 0, 0.5);
    --sidebar-bg: rgba(12, 17, 27, 0.72);
    --chart-text: #8a96ab;
    --chart-axis: rgba(148, 163, 184, 0.2);
    --chart-grid: rgba(148, 163, 184, 0.1);
    --chart-tooltip-bg: rgba(17, 23, 36, 0.96);
    --chart-tooltip-border: rgba(148, 163, 184, 0.2);
    --chart-tooltip-text: #e6ebf5;
    --chart-zoom-bg: rgba(148, 163, 184, 0.1);
    --lat-1-fill: #22c55e; --lat-1-ink: #052e16; --lat-1-text: #4ade80;
    --lat-2-fill: #f59e0b; --lat-2-ink: #3d2a00; --lat-2-text: #fbbf24;
    --lat-3-fill: #f97316; --lat-3-ink: #431407; --lat-3-text: #fb923c;
    --lat-4-fill: #ef4444; --lat-4-ink: #450a0a; --lat-4-text: #f87171;
    --lat-dead-fill: #b91c1c; --lat-dead-ink: #ffffff; --lat-dead-text: #f87171;
    --lat-none-fill: rgba(148, 163, 184, 0.12); --lat-none-ink: #6b7a90; --lat-none-text: #6b7a90;
    --lat-strip: rgba(3, 7, 18, 0.42);
    --lat-strip-track: rgba(255, 255, 255, 0.18);
    --loss-warning: #fbbf24;
    --loss-critical: #fca5a5;
    --loss-clear: rgba(255, 255, 255, 0.72);
  }
}
:root[data-theme="dark"] {
  color-scheme: dark;
  --bg: #0a0e17;
  --glow-1: rgba(59, 130, 246, 0.16);
  --glow-2: rgba(34, 211, 238, 0.1);
  --grid-dot: rgba(148, 163, 184, 0.09);
  --surface: rgba(17, 23, 36, 0.72);
  --surface-2: rgba(30, 38, 56, 0.55);
  --surface-solid: #111724;
  --text: #e6ebf5;
  --text-strong: #f8fafc;
  --muted: #8a96ab;
  --faint: #5b6a82;
  --line: rgba(148, 163, 184, 0.14);
  --line-strong: rgba(148, 163, 184, 0.28);
  --blue: #3b82f6;
  --cyan: #22d3ee;
  --green: #34d399;
  --yellow: #fbbf24;
  --red: #f87171;
  --orange: #fb923c;
  --track: rgba(148, 163, 184, 0.14);
  --hover: rgba(148, 163, 184, 0.07);
  --shadow: inset 0 1px 0 rgba(255, 255, 255, 0.03), 0 12px 32px rgba(0, 0, 0, 0.35);
  --shadow-hover: inset 0 1px 0 rgba(255, 255, 255, 0.05), 0 18px 44px rgba(0, 0, 0, 0.5);
  --sidebar-bg: rgba(12, 17, 27, 0.72);
  --chart-text: #8a96ab;
  --chart-axis: rgba(148, 163, 184, 0.2);
  --chart-grid: rgba(148, 163, 184, 0.1);
  --chart-tooltip-bg: rgba(17, 23, 36, 0.96);
  --chart-tooltip-border: rgba(148, 163, 184, 0.2);
  --chart-tooltip-text: #e6ebf5;
  --chart-zoom-bg: rgba(148, 163, 184, 0.1);
  --lat-1-fill: #22c55e; --lat-1-ink: #052e16; --lat-1-text: #4ade80;
  --lat-2-fill: #f59e0b; --lat-2-ink: #3d2a00; --lat-2-text: #fbbf24;
  --lat-3-fill: #f97316; --lat-3-ink: #431407; --lat-3-text: #fb923c;
  --lat-4-fill: #ef4444; --lat-4-ink: #450a0a; --lat-4-text: #f87171;
  --lat-dead-fill: #b91c1c; --lat-dead-ink: #ffffff; --lat-dead-text: #f87171;
  --lat-none-fill: rgba(148, 163, 184, 0.12); --lat-none-ink: #6b7a90; --lat-none-text: #6b7a90;
  --lat-strip: rgba(3, 7, 18, 0.42);
  --lat-strip-track: rgba(255, 255, 255, 0.18);
  --loss-warning: #fbbf24;
  --loss-critical: #fca5a5;
  --loss-clear: rgba(255, 255, 255, 0.72);
}

/* Derived tints. Mixed from the tokens above so they follow the theme without
   a second list of colours. */
:root {
  --accent: var(--blue);
  --accent-soft: color-mix(in srgb, var(--accent) 13%, transparent);
  --accent-border: color-mix(in srgb, var(--accent) 45%, transparent);
  --ok-soft: color-mix(in srgb, var(--green) 14%, transparent);
  --warn-soft: color-mix(in srgb, var(--yellow) 16%, transparent);
  --danger-soft: color-mix(in srgb, var(--red) 14%, transparent);
  --info-soft: color-mix(in srgb, var(--cyan) 14%, transparent);
  --gray-soft: color-mix(in srgb, var(--muted) 14%, transparent);
  --amber-soft: color-mix(in srgb, var(--orange) 15%, transparent);
}

/* ── Base ─────────────────────────────────────────────────────── */
*, *::before, *::after { box-sizing: border-box; }
html { -webkit-text-size-adjust: 100%; }
body {
  margin: 0;
  min-height: 100dvh;
  background: var(--bg);
  color: var(--text);
  font-family: var(--font-sans);
  font-size: 14px;
  line-height: 1.4;
  -webkit-font-smoothing: antialiased;
  text-rendering: optimizeLegibility;
}
button, input { font-family: inherit; }
:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; border-radius: 6px; }
.mono { font-family: var(--font-mono); font-variant-numeric: tabular-nums; }

/* The mount point is the container every layout rule below measures against,
   so the dashboard lays itself out for the width it was given rather than for
   the window — which is also what lets it be shown inside a frame of any size. */
#app { container: app / inline-size; min-height: 100dvh; }

/* The first paint, before the dashboard knows whether it is gated. It is the
   page background and nothing else, so whichever shell wins arrives on a
   surface that was already there rather than replacing a different one. */
.boot { min-height: var(--shell-h, 100dvh); background: var(--bg); }

.app {
  position: relative;
  display: grid;
  grid-template-columns: var(--sidebar-w) minmax(0, 1fr);
  min-height: var(--shell-h, 100dvh);
  background:
    radial-gradient(1100px 520px at 12% -8%, var(--glow-1), transparent 62%),
    radial-gradient(900px 480px at 92% -4%, var(--glow-2), transparent 58%),
    var(--bg);
  transition: background-color var(--dur) ease;
}
/* A faint dot grid under the top of the page: the texture probe dashboards
   have, kept far enough down the contrast range that it reads as paper, not
   as a pattern. It fades out well before the first scroll. */
.app::before {
  content: "";
  position: absolute; inset: 0; z-index: 0; pointer-events: none;
  background-image: radial-gradient(var(--grid-dot) 1px, transparent 1.2px);
  background-size: 26px 26px;
  mask-image: linear-gradient(180deg, rgba(0, 0, 0, 0.9), transparent 640px);
  -webkit-mask-image: linear-gradient(180deg, rgba(0, 0, 0, 0.9), transparent 640px);
}
.app > * { position: relative; z-index: 1; }

/* ── Sidebar ──────────────────────────────────────────────────── */
.sidebar {
  position: sticky; top: 0;
  height: var(--shell-h, 100dvh);
  display: flex; flex-direction: column;
  padding: 22px 14px calc(18px + env(safe-area-inset-bottom));
  border-right: 1px solid var(--line);
  background: var(--sidebar-bg);
  backdrop-filter: blur(18px);
  -webkit-backdrop-filter: blur(18px);
  overflow: hidden auto;
  scrollbar-width: none;
}
.brand { display: flex; align-items: center; gap: 12px; padding: 4px 8px 0; margin-bottom: 26px; min-height: 40px; }
.brand-logo {
  width: 40px; height: 40px; flex-shrink: 0; display: grid; place-items: center;
  color: white; border-radius: 12px;
  background: linear-gradient(135deg, var(--blue), var(--cyan));
  box-shadow: 0 10px 24px color-mix(in srgb, var(--blue) 35%, transparent);
}
.brand-logo svg { width: 22px; height: 22px; }
.brand-text { min-width: 0; line-height: 1.1; }
.brand-text strong { display: block; font-size: 15px; font-weight: 800; letter-spacing: -0.01em; color: var(--text-strong); white-space: nowrap; }
.brand-text span {
  display: block; margin-top: 3px; color: var(--muted);
  font-family: var(--font-mono); font-size: 10.5px; letter-spacing: 0.08em; text-transform: uppercase;
}
.nav { display: flex; flex-direction: column; gap: 4px; }
.nav-item {
  position: relative;
  display: flex; align-items: center; gap: 12px; height: 42px; padding: 0 12px;
  color: var(--muted); border-radius: var(--radius-sm); text-decoration: none;
  font-size: 13.5px; font-weight: 650; cursor: pointer; white-space: nowrap;
  transition: color var(--dur) ease, background-color var(--dur) ease;
}
.nav-item svg { width: 19px; height: 19px; flex-shrink: 0; }
.nav-item:hover { color: var(--text); background: var(--hover); }
.nav-item.active { color: var(--accent); background: var(--accent-soft); }
/* The active mark is a short bar on the left edge, the way a rail highlights
   its current stop; it scales in rather than appearing. */
.nav-item::before {
  content: ""; position: absolute; left: 0; top: 50%; width: 3px; height: 18px;
  border-radius: 999px; background: var(--accent);
  transform: translateY(-50%) scaleY(0); transform-origin: center;
  transition: transform var(--dur) var(--ease);
}
.nav-item.active::before { transform: translateY(-50%) scaleY(1); }
.nav-item .nav-label { overflow: hidden; text-overflow: ellipsis; }

.fleet {
  margin-top: auto; padding: 14px 14px 12px;
  border: 1px solid var(--line); border-radius: var(--radius);
  background: var(--surface);
}
.fleet-head {
  display: flex; align-items: center; justify-content: space-between; gap: 8px;
  color: var(--muted); font-family: var(--font-mono); font-size: 10.5px;
  letter-spacing: 0.08em; text-transform: uppercase;
}
.fleet-count { margin-top: 8px; display: flex; align-items: baseline; gap: 6px; }
.fleet-count strong { font-size: 22px; font-weight: 800; letter-spacing: -0.02em; font-variant-numeric: tabular-nums; color: var(--text-strong); }
.fleet-count span { color: var(--muted); font-size: 12px; font-weight: 600; }
.fleet-line {
  margin-top: 8px; display: flex; align-items: center; gap: 7px;
  color: var(--muted); font-size: 12px; font-weight: 600; font-variant-numeric: tabular-nums;
}
.fleet-line .dot { color: var(--green); }
.fleet-line em { font-style: normal; color: var(--text); }
.fleet-line.quiet { color: var(--faint); font-size: 11px; }

/* ── Main ──────────────────────────────────────────────────────── */
.main {
  min-width: 0; padding: 22px clamp(14px, 2.4cqw, 30px) 28px;
  padding-left: max(clamp(14px, 2.4cqw, 30px), env(safe-area-inset-left));
  padding-right: max(clamp(14px, 2.4cqw, 30px), env(safe-area-inset-right));
}
.topbar {
  display: flex; align-items: center; justify-content: space-between;
  flex-wrap: wrap; gap: 12px 18px; margin-bottom: 18px;
}
.title { min-width: 0; }
.page-title {
  margin: 0; font-size: clamp(22px, 2.4cqw, 28px); font-weight: 800;
  letter-spacing: -0.02em; line-height: 1.15; color: var(--text-strong);
}
.page-sub { margin: 4px 0 0; color: var(--muted); font-size: 13px; overflow-wrap: anywhere; min-height: 18px; }
.page-sub.error { color: var(--red); font-weight: 600; }
.toolbar { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; justify-content: flex-end; margin-left: auto; }
.chip {
  display: inline-flex; align-items: center; gap: 7px;
  border: 1px solid var(--line); background: var(--surface);
  border-radius: 999px; padding: 0 12px; height: 34px; color: var(--muted);
  font-size: 12.5px; font-weight: 650; box-shadow: var(--shadow);
  line-height: 1; white-space: nowrap;
  transition: color var(--dur) ease, border-color var(--dur) ease, background-color var(--dur) ease;
}
.icon-chip { width: 34px; padding: 0; justify-content: center; cursor: pointer; }
.icon-chip svg { width: 16px; height: 16px; }
.icon-chip:hover { color: var(--accent); border-color: var(--accent-border); }
.live-chip { font-family: var(--font-mono); font-variant-numeric: tabular-nums; }
.live-chip .dot { color: var(--green); }
.live-chip .live-label { color: var(--text); font-weight: 700; letter-spacing: 0.02em; }
.live-chip .live-ago { color: var(--faint); }
.live-chip.stale .dot { color: var(--red); }
.live-chip.stale .dot::before { animation: none; transform: scale(2); opacity: 0.18; }

/* ── Phone tab strip ───────────────────────────────────────────── */
/* Five tabs do not fit a phone's width, and letting them push past it made the
   whole document scroll sideways — so every vertical swipe on the page drifted.
   The strip scrolls on its own instead. */
.tabbar {
  display: none; gap: 4px; padding: 4px;
  margin: -6px 0 16px; max-width: 100%; overflow-x: auto; scrollbar-width: none;
  border: 1px solid var(--line); border-radius: 12px; background: var(--surface);
}
.tabbar::-webkit-scrollbar { display: none; }
.tabbar button {
  flex: 1 0 auto; min-height: 36px; padding: 0 14px; white-space: nowrap;
  border: 0; border-radius: 9px; background: transparent; color: var(--muted);
  font-size: 13px; font-weight: 700; cursor: pointer;
  transition: color var(--dur) ease, background-color var(--dur) ease;
}
.tabbar button.active { color: var(--accent); background: var(--accent-soft); }

/* ── Page transition ───────────────────────────────────────────── */
.page { min-width: 0; }
.page-enter-active { transition: opacity 0.22s var(--ease), transform 0.22s var(--ease); }
.page-leave-active { transition: opacity 0.12s ease, transform 0.12s ease; }
.page-enter-from { opacity: 0; transform: translateY(6px); }
.page-leave-to { opacity: 0; transform: translateY(-4px); }

/* ── Menu picker ──────────────────────────────────────────────────
   One dropdown vocabulary, used by every picker on the page: a chip carrying
   the current value and a popover of options headed by what is being chosen. */
.menu-picker { position: relative; }
.menu-chip {
  font-family: var(--font-mono); font-size: 12.5px; font-weight: 650; font-variant-numeric: tabular-nums;
  cursor: pointer; color: var(--text);
}
.menu-chip:hover, .menu-chip.open { color: var(--accent); border-color: var(--accent-border); }
.menu-chip svg { width: 12px; height: 12px; flex-shrink: 0; color: var(--muted); transition: transform 0.2s var(--ease); }
.menu-chip.open svg { transform: rotate(180deg); }
.menu-pop {
  position: absolute; top: calc(100% + 8px); right: 0; z-index: 600;
  width: 232px; overflow: hidden;
  background: var(--surface-solid); border: 1px solid var(--line-strong); border-radius: 14px;
  box-shadow: var(--shadow-hover);
  transform-origin: top right;
  animation: popIn 0.16s var(--ease);
}
@keyframes popIn { from { opacity: 0; transform: translateY(-4px) scale(0.98); } }
.menu-pop-head {
  padding: 11px 14px 9px; border-bottom: 1px solid var(--line);
  color: var(--muted); font-family: var(--font-mono); font-size: 10.5px;
  letter-spacing: 0.08em; text-transform: uppercase;
}
.menu-pop-list { max-height: 296px; overflow-y: auto; padding: 6px; overscroll-behavior: contain; }
.menu-option {
  display: flex; align-items: center; justify-content: space-between; gap: 10px;
  width: 100%; border: none; border-radius: 9px; padding: 8px 10px;
  background: transparent; color: var(--text); text-align: left;
  font: inherit; font-size: 13px; font-weight: 600; font-variant-numeric: tabular-nums;
  cursor: pointer; transition: background-color var(--dur) ease, color var(--dur) ease;
}
.menu-option:hover { background: var(--hover); }
.menu-option.active { background: var(--accent-soft); color: var(--accent); }
.menu-note { color: var(--muted); font-size: 11px; font-weight: 600; white-space: nowrap; }
.menu-option.active .menu-note { color: inherit; opacity: 0.75; }

/* ── Status dots ──────────────────────────────────────────────── */
.dot {
  position: relative; flex-shrink: 0;
  width: 8px; height: 8px; border-radius: 999px; background: currentColor;
}
/* Halo pulses via transform/opacity: box-shadow spread snaps to device
   pixels on an element this small, which reads as jitter. */
.dot::before {
  content: ""; position: absolute; inset: 0; border-radius: inherit;
  background: currentColor;
  animation: pulseDot 2.4s ease-in-out infinite;
}
@keyframes pulseDot {
  0%, 100% { transform: scale(1.75); opacity: 0.18; }
  50% { transform: scale(2.75); opacity: 0.07; }
}
/* A card head reports state as a dot rather than as words the body already
   spells out. Shared by the latency and relay cards. */
.dot-only { width: 10px; height: 10px; border-radius: 999px; flex-shrink: 0; margin-top: 6px; position: relative; }
.dot-only::before {
  content: ""; position: absolute; inset: 0; border-radius: inherit;
  background: currentColor; animation: pulseDot 2.4s ease-in-out infinite;
}
.dot-only.ok { background: var(--green); color: var(--green); }
.dot-only.warn { background: var(--yellow); color: var(--yellow); }
.dot-only.danger { background: var(--red); color: var(--red); }
.dot-only.gray { background: var(--faint); color: var(--faint); }
.dot-only.gray::before { animation: none; }

/* ── Grids ─────────────────────────────────────────────────────── */
/* Every grid sizes itself from the width it has: four tiles become two on a
   phone, node cards go three abreast on a wide screen and one on a narrow one,
   with no breakpoint deciding it. */
.grid { display: grid; grid-template-columns: repeat(12, minmax(0, 1fr)); gap: 14px; }
.tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 200px), 1fr)); gap: 14px; }
.tiles.tiles-3 { grid-template-columns: repeat(auto-fit, minmax(min(100%, 108px), 1fr)); }
.nodes { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(100%, 400px), 1fr)); gap: 14px; }
.sources { margin-top: 14px; }
.span-12 { grid-column: 1 / -1; }
.span-6 { grid-column: span 6; }

/* ── Cards ─────────────────────────────────────────────────────── */
.card {
  position: relative; min-width: 0;
  background: var(--surface); border: 1px solid var(--line);
  border-radius: var(--radius); box-shadow: var(--shadow); padding: 18px;
  animation: cardIn 0.4s var(--ease) both;
  transition: border-color var(--dur) ease, box-shadow var(--dur) ease, transform var(--dur) ease, background-color var(--dur) ease;
}
.tiles > .card:nth-child(2), .nodes > .card:nth-child(2) { animation-delay: 0.04s; }
.tiles > .card:nth-child(3), .nodes > .card:nth-child(3) { animation-delay: 0.08s; }
.tiles > .card:nth-child(4), .nodes > .card:nth-child(4) { animation-delay: 0.12s; }
.nodes > .card:nth-child(5) { animation-delay: 0.16s; }
.nodes > .card:nth-child(6) { animation-delay: 0.2s; }
.nodes > .card:nth-child(n + 7) { animation-delay: 0.24s; }
@keyframes cardIn { from { opacity: 0; transform: translateY(10px); } }
.clickable { cursor: pointer; }
@media (hover: hover) {
  .clickable:hover { transform: translateY(-2px); border-color: var(--line-strong); box-shadow: var(--shadow-hover); }
}
.clickable:active { transform: translateY(0); transition-duration: 60ms; }

/* Tiles: the row of headline figures at the top of the overview pages. */
.tile { display: flex; flex-direction: column; padding: 16px; container: tile / inline-size; }
.tile > .progress { margin-top: auto; }
.metric-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; margin-bottom: 14px; }
.metric-head > div:first-child { min-width: 0; }
.metric-side { display: flex; flex-direction: column; align-items: flex-end; gap: 8px; flex-shrink: 0; }
.eyebrow {
  margin: 0 0 8px; color: var(--muted); font-family: var(--font-mono);
  font-size: 10.5px; letter-spacing: 0.08em; line-height: 1; text-transform: uppercase;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.metric-value {
  margin: 0; font-size: clamp(20px, 9cqw, 26px); font-weight: 800; letter-spacing: -0.02em;
  font-variant-numeric: tabular-nums; line-height: 1.1; color: var(--text-strong); overflow-wrap: anywhere;
}
.metric-value.small { font-size: 18px; }
.metric-detail { margin: 6px 0 0; min-height: 14px; color: var(--muted); font-size: 12px; font-weight: 600; font-variant-numeric: tabular-nums; }
/* The tile's own affordance: a chevron that slides in on hover, since a tile
   is too narrow to carry the words a node card does. */
.tile-go {
  width: 26px; height: 26px; display: grid; place-items: center; flex-shrink: 0;
  border-radius: 8px; color: var(--faint); background: transparent;
  transition: color var(--dur) ease, background-color var(--dur) ease, transform var(--dur) var(--ease);
}
.tile-go svg { width: 14px; height: 14px; }
.clickable:hover .tile-go { color: var(--accent); background: var(--accent-soft); transform: translateX(2px); }
@container tile (max-width: 190px) {
  .metric-side .delta { font-size: 11px; padding: 4px 7px; }
}
/* A tile a third of a phone wide keeps its figure and its badge; the footnote
   and the chevron are the first things to go. */
@container tile (max-width: 150px) {
  .metric-detail, .tile-go { display: none; }
  .metric-head { flex-direction: column; align-items: flex-start; margin-bottom: 8px; gap: 8px; }
  .metric-side { flex-direction: row; align-items: center; }
  .eyebrow { font-size: 10px; letter-spacing: 0.04em; margin-bottom: 6px; }
  .metric-value { font-size: 20px; white-space: nowrap; overflow-wrap: normal; }
}

/* ── Badges ────────────────────────────────────────────────────── */
.delta, .tag, .status {
  display: inline-flex; align-items: center; gap: 6px;
  border-radius: 999px; padding: 5px 9px;
  font-size: 11.5px; font-weight: 750; line-height: 1; white-space: nowrap;
  font-variant-numeric: tabular-nums;
}
.delta { background: var(--ok-soft); color: var(--green); }
.delta.warn { background: var(--warn-soft); color: var(--yellow); }
.delta.danger { background: var(--danger-soft); color: var(--red); }
.tag.red { background: var(--danger-soft); color: var(--red); }
.status { color: var(--green); background: var(--ok-soft); letter-spacing: 0.04em; text-transform: uppercase; font-size: 10.5px; }
.status.warn { color: var(--yellow); background: var(--warn-soft); }
.status.danger { color: var(--red); background: var(--danger-soft); }
.status.gray { color: var(--muted); background: var(--gray-soft); }
.status.gray .dot::before { animation: none; transform: scale(2); opacity: 0.16; }

.view-trend {
  display: inline-flex; align-items: center; gap: 4px; flex-shrink: 0;
  color: var(--muted); font-size: 12px; font-weight: 700; line-height: 1; white-space: nowrap;
  transition: color var(--dur) ease;
}
.view-trend svg { width: 13px; height: 13px; transition: transform var(--dur) var(--ease); }
.clickable:hover .view-trend { color: var(--accent); }
.clickable:hover .view-trend svg { transform: translateX(2px); }

/* ── Progress bar ──────────────────────────────────────────────── */
.progress {
  --value: 0; --bar: var(--accent);
  height: 6px; position: relative; overflow: hidden;
  border-radius: 999px; background: var(--track);
}
.progress::after {
  content: ""; position: absolute; inset: 0 auto 0 0;
  width: calc(var(--value) * 1%); border-radius: inherit;
  background: linear-gradient(90deg, color-mix(in srgb, var(--bar) 80%, var(--surface-solid)), var(--bar));
  box-shadow: 0 0 12px color-mix(in srgb, var(--bar) 45%, transparent);
  animation: grow 0.8s var(--ease) both;
  transition: width 0.6s var(--ease), background-color 0.3s ease;
}
@keyframes grow { from { width: 0 } to { width: calc(var(--value) * 1%) } }
.progress.empty::after { width: 0; }
.progress.thin { height: 4px; }

/* ── Node cards ────────────────────────────────────────────────── */
.node-card { display: flex; flex-direction: column; gap: 14px; container: node / inline-size; }
.node-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.node-title { min-width: 0; display: flex; align-items: flex-start; gap: 10px; }
.node-title .dot-only { margin-top: 5px; }
.node-name {
  margin: 0; font-size: 15px; font-weight: 750; letter-spacing: -0.01em;
  color: var(--text-strong); overflow-wrap: anywhere; line-height: 1.2;
}
.node-meta {
  margin: 4px 0 0; color: var(--muted); font-family: var(--font-mono);
  font-size: 11px; letter-spacing: 0.01em; font-variant-numeric: tabular-nums;
  display: flex; flex-wrap: wrap; gap: 4px 10px;
}
.node-meta span { white-space: nowrap; }
.node-side { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }
.node-foot {
  display: flex; align-items: center; justify-content: space-between; gap: 10px;
  padding-top: 12px; border-top: 1px solid var(--line);
  color: var(--faint); font-family: var(--font-mono); font-size: 11px; font-variant-numeric: tabular-nums;
}
.node-foot .view-trend { margin-left: auto; }

/* ── Traffic card body ─────────────────────────────────────────── */
.tc-body { display: flex; align-items: center; gap: 18px; }
.tc-body > .gauge { flex-shrink: 0; }
.usage-rows { flex: 1; min-width: 0; display: grid; gap: 10px; }
.usage-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 5px 10px; align-items: center; }
.usage-row .progress { grid-column: 1 / -1; }
.row-label { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
.row-label strong { font-family: var(--font-mono); font-size: 11px; letter-spacing: 0.06em; color: var(--muted); }
.row-label span { color: var(--text); font-size: 12px; font-weight: 600; font-variant-numeric: tabular-nums; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.percent { font-size: 12.5px; font-weight: 800; font-variant-numeric: tabular-nums; text-align: right; color: var(--text); }
.percent.warn { color: var(--yellow); }
.percent.danger { color: var(--red); }
.percent.unlimited { color: var(--faint); font-weight: 700; }

/* ── Rings ─────────────────────────────────────────────────────── */
.gauges { flex: 1 1 auto; display: flex; flex-wrap: wrap; justify-content: space-around; gap: 14px 18px; }
.gauge { display: flex; flex-direction: column; align-items: center; text-align: center; min-width: 76px; }
.ring-wrap { position: relative; width: 84px; height: 84px; }
.ring { width: 100%; height: 100%; transform: rotate(-90deg); }
.ring-bg { fill: none; stroke: var(--track); stroke-width: 7; }
.ring-fg {
  fill: none; stroke-width: 7; stroke-linecap: round;
  stroke-dasharray: 213.63;
  transition: stroke-dashoffset 0.8s var(--ease), stroke 0.3s;
  animation: ringIn 1s var(--ease) both;
}
@keyframes ringIn { from { stroke-dashoffset: 213.63; } }
.ring-value {
  position: absolute; inset: 0; display: grid; place-items: center;
  font-size: 15px; font-weight: 800; font-variant-numeric: tabular-nums; color: var(--text-strong);
}
.ring-value.warn { color: var(--yellow); }
.ring-value.danger { color: var(--red); }
.ring-value.infinite { font-size: 22px; font-weight: 700; color: var(--muted); }
.gauge-label {
  margin-top: 7px; color: var(--muted); font-family: var(--font-mono);
  font-size: 10.5px; letter-spacing: 0.08em; text-transform: uppercase;
}
.gauge-detail {
  margin-top: 3px; min-height: 14px; color: var(--faint);
  font-size: 11px; font-weight: 600; font-variant-numeric: tabular-nums;
}
.tc-body .ring-wrap { width: 78px; height: 78px; }

/* ── Resource card body ────────────────────────────────────────── */
.rc-body { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 14px 24px; }
.io-block { display: flex; flex-direction: row; gap: 8px; flex: 1 1 100%; min-width: 0; }
.io-block .io-stat { flex: 1; min-width: 0; }
@container node (min-width: 560px) {
  .io-block { flex-direction: column; flex: 0 0 auto; min-width: 164px; }
}
.io-stat {
  display: flex; align-items: center; gap: 10px;
  border: 1px solid var(--line); border-radius: var(--radius-sm); padding: 8px 12px;
  background: var(--surface-2);
}
.io-icon {
  width: 28px; height: 28px; display: grid; place-items: center;
  border-radius: 8px; font-size: 14px; font-weight: 800; flex-shrink: 0;
}
.io-icon.read { background: var(--ok-soft); color: var(--green); }
.io-icon.write { background: var(--amber-soft); color: var(--orange); }
.io-label {
  display: block; margin-bottom: 2px; color: var(--muted);
  font-family: var(--font-mono); font-size: 10px; letter-spacing: 0.08em; text-transform: uppercase;
}
.io-stat strong { font-size: 13.5px; font-variant-numeric: tabular-nums; color: var(--text-strong); white-space: nowrap; }
.no-data { color: var(--muted); font-size: 13.5px; padding: 8px 0; margin: 0; }

/* The card's width, not the window's, decides when the ring gives up its
   column: a card that is one of three across is as narrow as a phone's. */
@container node (max-width: 360px) {
  .tc-body { gap: 14px; }
  .tc-body .ring-wrap { width: 64px; height: 64px; }
  .tc-body .ring-value { font-size: 13px; }
  .tc-body .gauge-label { display: none; }
  .rc-body { gap: 12px; }
  .ring-wrap { width: 68px; height: 68px; }
  .ring-value { font-size: 13px; }
  .gauge { min-width: 64px; }
}

/* ── Skeleton ─────────────────────────────────────────────────── */
/* What a page shows while it waits on its nodes: the shape of the card that is
   coming, in the surface colour, with a sheen moving across it so it reads as
   loading rather than as empty. */
.skeleton { position: relative; overflow: hidden; border-radius: 8px; background: var(--track); min-height: 12px; }
.skeleton::after {
  content: ""; position: absolute; inset: 0;
  background: linear-gradient(90deg, transparent, color-mix(in srgb, var(--text) 8%, transparent), transparent);
  transform: translateX(-100%); animation: sheen 1.4s ease-in-out infinite;
}
@keyframes sheen { to { transform: translateX(100%); } }
.skeleton-card { display: flex; flex-direction: column; gap: 12px; }
.skeleton-card .skeleton.w40 { width: 40%; height: 14px; }
.skeleton-card .skeleton.w70 { width: 70%; height: 24px; }
.skeleton-card .skeleton.block { height: 120px; }

/* ── Modal ─────────────────────────────────────────────────────── */
.modal-backdrop {
  position: fixed; inset: 0; z-index: 1000;
  background: rgba(3, 7, 18, 0.55); backdrop-filter: blur(6px); -webkit-backdrop-filter: blur(6px);
  display: grid; place-items: center; padding: 16px;
  animation: fadeIn 0.2s ease;
}
.modal-content {
  position: relative; width: min(100%, 1040px); max-height: min(100%, 88dvh);
  display: flex; flex-direction: column; overflow: hidden;
  background: var(--surface-solid); border: 1px solid var(--line-strong);
  border-radius: var(--radius-lg); box-shadow: 0 30px 80px rgba(0, 0, 0, 0.35);
  animation: modalIn 0.28s var(--ease);
}
.modal-body { min-height: 0; overflow: auto; }
@keyframes fadeIn { from { opacity: 0 } to { opacity: 1 } }
@keyframes modalIn { from { opacity: 0; transform: translateY(14px) scale(0.985) } to { opacity: 1; transform: none } }

.modal-header {
  display: flex; justify-content: space-between; align-items: flex-start;
  padding: 20px 60px 12px 24px; gap: 14px; flex-wrap: wrap;
}
.modal-title { margin: 0; font-size: 18px; font-weight: 800; letter-spacing: -0.01em; color: var(--text-strong); overflow-wrap: anywhere; }
.modal-subtitle { margin: 4px 0 0; color: var(--muted); font-size: 13px; overflow-wrap: anywhere; }
.modal-controls { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }

.toggle-group {
  display: inline-flex; padding: 3px; gap: 2px;
  border: 1px solid var(--line); border-radius: 10px; background: var(--surface-2);
  max-width: 100%; overflow-x: auto; scrollbar-width: none; -webkit-overflow-scrolling: touch;
}
.toggle-group::-webkit-scrollbar { display: none; }
.toggle-group button {
  flex-shrink: 0; border: none; border-radius: 7px; background: transparent; padding: 6px 12px;
  font-size: 12.5px; font-weight: 700; cursor: pointer; color: var(--muted); white-space: nowrap;
  transition: background-color var(--dur) ease, color var(--dur) ease, box-shadow var(--dur) ease;
}
.toggle-group button.active { background: var(--surface-solid); color: var(--accent); box-shadow: var(--shadow); }
.toggle-group button:hover:not(.active) { color: var(--text); }

.close-btn {
  position: absolute; top: 14px; right: 14px; z-index: 1;
  width: 34px; height: 34px; display: grid; place-items: center; padding: 0;
  border: 1px solid var(--line); border-radius: 999px; background: var(--surface-2);
  font-size: 20px; line-height: 1; cursor: pointer; color: var(--muted);
  transition: background-color var(--dur) ease, color var(--dur) ease, transform var(--dur) var(--ease);
}
.close-btn:hover { color: var(--text); border-color: var(--line-strong); transform: rotate(90deg); }

.chart-container { width: 100%; height: clamp(300px, 52vh, 460px); padding: 6px 12px 16px; }
.chart-loading { padding: 60px 20px; text-align: center; color: var(--muted); font-size: 14px; }

.footer-note { margin-top: 16px; color: var(--faint); font-size: 12px; text-align: right; min-height: 18px; }

/* ── Narrower shells ───────────────────────────────────────────── */
/* Between a laptop and a phone the sidebar keeps its stops but loses its
   words: the icons are the navigation and the labels ride in their titles. */
@container app (max-width: 1099px) {
  .app { grid-template-columns: var(--rail-w) minmax(0, 1fr); }
  .sidebar { padding-left: 10px; padding-right: 10px; }
  .brand { justify-content: center; padding: 4px 0 0; }
  .brand-text, .nav-item .nav-label, .fleet-head, .fleet-count span, .fleet-line { display: none; }
  .nav-item { justify-content: center; padding: 0; gap: 0; }
  .fleet { padding: 10px 6px; text-align: center; }
  .fleet-count { justify-content: center; margin-top: 0; }
  .fleet-count strong { font-size: 15px; }
  .fleet-count.rail-only { display: flex; }
}
@container app (max-width: 759px) {
  .app { grid-template-columns: minmax(0, 1fr); }
  .sidebar { display: none; }
  .main { padding-top: 14px; }
  .topbar { margin-bottom: 12px; }
  .page-sub { display: none; }
  .tabbar {
    display: flex; position: sticky; top: 0; z-index: 30;
    margin: 0 0 14px;
    backdrop-filter: blur(14px); -webkit-backdrop-filter: blur(14px);
    box-shadow: var(--shadow);
  }
  .live-chip .live-label { display: none; }
  .chip { height: 32px; padding: 0 10px; font-size: 12px; }
  .icon-chip { width: 32px; }
  .card { padding: 14px; border-radius: 12px; }
  .grid, .tiles, .nodes { gap: 10px; }
  .sources { margin-top: 10px; }
  .tiles { grid-template-columns: repeat(auto-fit, minmax(min(100%, 150px), 1fr)); }
  .tile { padding: 13px; }
  .metric-head { margin-bottom: 10px; }
  .footer-note { text-align: left; }
  /* The modal is a sheet from the bottom of the screen, where a thumb can
     reach its controls, rather than a window floating in the middle. */
  .modal-backdrop { place-items: end center; padding: 0; }
  .modal-content {
    width: 100%; max-height: min(100%, 92dvh);
    border-radius: 18px 18px 0 0; border-bottom: 0;
    padding-bottom: env(safe-area-inset-bottom);
    animation: sheetUp 0.32s var(--ease);
  }
  .modal-content::before {
    content: ""; display: block; width: 38px; height: 4px; margin: 8px auto 0;
    border-radius: 999px; background: var(--line-strong);
  }
  .modal-header { flex-direction: column; padding: 8px 56px 8px 16px; gap: 10px; }
  .modal-controls { width: 100%; }
  .toggle-group button { padding: 6px 10px; font-size: 12px; }
  .close-btn { top: 10px; right: 12px; }
  .chart-container { height: clamp(260px, 46vh, 400px); padding: 4px 4px 10px; }
}
@keyframes sheetUp { from { transform: translateY(24px); opacity: 0.6 } to { transform: none; opacity: 1 } }

/* Fingers need bigger targets than pointers do. */
@media (pointer: coarse) {
  .nav-item, .menu-option, .tabbar button { min-height: 44px; }
  .chip, .icon-chip { height: 36px; }
}

/* ── Motion ────────────────────────────────────────────────────── */
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 0.01ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 0.01ms !important;
  }
  .clickable:hover { transform: none; }
}
</style>
