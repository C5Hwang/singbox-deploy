<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, shallowRef, watch } from "vue";
import SegmentedControl from "../components/SegmentedControl.vue";
import { authKeyCryptoAvailable, encryptAuthKey } from "../authkey";
import {
  authkeyError,
  buildLinks,
  copyText,
  domainError,
  hostnameError,
  missingInput,
  portError,
  subscriptionForm as form,
  subscriptionToken,
  type SubscriptionLink,
  type SubscriptionMode,
  type TailscaleRole,
} from "../subscriptions";

// Builds the subscription links for a salt, in this browser. Nothing here asks
// the server anything: the token is md5(salt + "\n") exactly as the hub
// derives it, so the page works whether or not the hub is reachable, and the
// salt never leaves the tab it was typed into.

const MODES: { value: SubscriptionMode; label: string }[] = [
  { value: "standard", label: "Standard" },
  { value: "tailscale", label: "Tailscale" },
];
const ROLES: { value: TailscaleRole; label: string }[] = [
  { value: "server", label: "Server" },
  { value: "client", label: "Client" },
];

const tailscale = computed(() => form.mode === "tailscale");
const client = computed(() => tailscale.value && form.role === "client");

// A field says what is wrong with it only once it has been left: a domain is
// not wrong while it is still being typed. After that the hint follows the
// value, so it clears the moment the value is fixed.
const touched = reactive({ domain: false, port: false, hostname: false, authkey: false });
const errors = computed(() => ({
  domain: touched.domain ? domainError(form.domain) : "",
  port: touched.port ? portError(form.port) : "",
  hostname: touched.hostname ? hostnameError(form.hostname) : "",
  authkey: touched.authkey ? authkeyError(form.authkey) : "",
}));

// The hint's text outlives the error by the length of its collapse, so the
// line closes over the words it was showing rather than over an empty box.
type Field = keyof typeof touched;
const hintText = reactive<Record<Field, string>>({ domain: "", port: "", hostname: "", authkey: "" });
watch(
  errors,
  (now) => {
    for (const field of Object.keys(now) as Field[]) if (now[field]) hintText[field] = now[field];
  },
  { immediate: true },
);

const showSalt = ref(false);
const showAuthkey = ref(false);

const token = computed(() => (form.salt.trim() ? subscriptionToken(form.salt) : ""));

// A Client link carries the auth key sealed under the salt, and sealing is
// asynchronous. It runs only when what goes into it changes — the salt, the
// hostname or the key — so a new domain or port rebuilds the links from the
// sealed value already in hand. Every seal draws a new nonce, so sealing on
// each keystroke would make the link churn while it is typed: a change made
// while typing waits for a pause first. Each run is numbered, and only the
// latest may land: a slow run for an older key never overwrites a newer one.
// The sealed value also names the input it was made from, and is used only
// while that is still the form's input.
const SEAL_DEBOUNCE_MS = 150;
const canSeal = authKeyCryptoAvailable();
const sealInput = computed(() => {
  if (!client.value || missingInput(form)) return "";
  return JSON.stringify([form.salt.trim(), form.hostname.trim(), form.authkey.trim()]);
});
const sealed = shallowRef<{ input: string; value: string } | null>(null);
const sealFailed = ref(false);
let sealRun = 0;
let sealTimer: number | undefined;
watch(
  sealInput,
  (input, before) => {
    const run = ++sealRun;
    window.clearTimeout(sealTimer);
    sealFailed.value = false;
    // Coming back to an input already sealed — a trip to Server and back —
    // keeps the link it had rather than drawing a new nonce for it.
    if (!input || !canSeal || sealed.value?.input === input) return;
    const [salt, hostname, authkey] = JSON.parse(input) as string[];
    const seal = () =>
      encryptAuthKey(salt, hostname, authkey).then(
        (value) => {
          if (run === sealRun) sealed.value = { input, value };
        },
        () => {
          if (run === sealRun) sealFailed.value = true;
        },
      );
    // The form becoming complete — the last field filled, or the switch to
    // Client — is not typing, and seals at once.
    if (before) sealTimer = window.setTimeout(seal, SEAL_DEBOUNCE_MS);
    else void seal();
  },
  { immediate: true },
);

// The links as they stand, or null while the auth key they need is still
// being sealed. Meanwhile the list keeps what it showed — the wait is a
// moment, and emptying the list for it would flicker on every keystroke.
const fresh = computed<SubscriptionLink[] | null>(() => {
  if (!client.value || missingInput(form)) return buildLinks(form);
  if (!canSeal || sealFailed.value) return [];
  const now = sealed.value;
  return now && now.input === sealInput.value ? buildLinks(form, now.value) : null;
});
const links = shallowRef<SubscriptionLink[]>([]);
watch(
  fresh,
  (now) => {
    if (now) links.value = now;
  },
  { immediate: true },
);
const sealing = computed(() => fresh.value === null);

const waitingOn = computed(() => {
  const missing = missingInput(form);
  if (missing || !client.value) return missing;
  if (!canSeal) return "Encrypting the auth key needs HTTPS. Open the dashboard over https://.";
  if (sealFailed.value) return "Could not encrypt the auth key in this browser.";
  return "Encrypting the auth key…";
});

const modeLabel = computed(() => {
  if (!tailscale.value) return "Standard";
  return form.role === "server" ? "Tailscale · Server" : "Tailscale · Client";
});

// Each row's URL is drawn in three parts — where, which token, and what the
// query adds. The sealed auth key is the one part not drawn whole: it is long
// and reads as noise, so the row shows its two ends, and the copy button hands
// out the full link. The token is read out of the link rather than taken from
// the salt field, since a row held while a new key is sealed was built for the
// salt before. The query is split at each parameter so a narrow row breaks
// between parameters rather than in the middle of one, and the address is
// split after the server and after the format directory for the same reason:
// a phone wraps it at a slash, not inside a word.
const SEALED_PARAM = "&authkey=";
function shortQueryPiece(piece: string): string {
  if (!piece.startsWith(SEALED_PARAM)) return piece;
  const value = piece.slice(SEALED_PARAM.length);
  if (value.length <= 16) return piece;
  return `${SEALED_PARAM}${value.slice(0, 7)}…${value.slice(-4)}`;
}
const rows = computed(() =>
  links.value.map((link) => {
    const dirPath = `/s/${link.dir}/`;
    const at = link.url.indexOf(dirPath) + dirPath.length;
    const end = link.url.indexOf("?", at);
    const base = link.url.slice(0, at);
    const cut = base.lastIndexOf("/", base.length - 2) + 1;
    const query = end < 0 ? "" : link.url.slice(end);
    return {
      link,
      base: [base.slice(0, cut), base.slice(cut)],
      token: link.url.slice(at, end < 0 ? undefined : end),
      query: query ? query.split(/(?=&)/).map(shortQueryPiece) : [],
    };
  }),
);

// A row whose URL changes under it briefly lights up, so a change of mode or
// role shows which links it touched. The counter is the element's key: a new
// value remounts the line and replays the highlight from the start.
const flashes = reactive<Record<string, number>>({});
watch(links, (now, before) => {
  const previous = new Map(before.map((link) => [link.dir, link.url]));
  for (const link of now) {
    const was = previous.get(link.dir);
    if (was !== undefined && was !== link.url) flashes[link.dir] = (flashes[link.dir] ?? 0) + 1;
  }
});

const COPIED_MS = 1500;
const TOAST_MS = 2600;
const copiedDir = ref("");
const toast = ref<{ ok: boolean; text: string } | null>(null);
let copiedTimer: number | undefined;
let toastTimer: number | undefined;

async function copy(link: SubscriptionLink) {
  // While a new auth key is being sealed the row still shows the old link,
  // which is no longer the one to hand out.
  if (sealing.value) return;
  const ok = await copyText(link.url);
  if (ok) {
    copiedDir.value = link.dir;
    window.clearTimeout(copiedTimer);
    copiedTimer = window.setTimeout(() => (copiedDir.value = ""), COPIED_MS);
  }
  toast.value = ok
    ? { ok, text: `${link.label} link copied.` }
    : { ok, text: "Could not copy. Select the link and copy it by hand." };
  window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => (toast.value = null), TOAST_MS);
}

onUnmounted(() => {
  window.clearTimeout(sealTimer);
  window.clearTimeout(copiedTimer);
  window.clearTimeout(toastTimer);
});
</script>

<template>
  <div class="subs">
    <form class="card subs-form" autocomplete="off" @submit.prevent>
      <div class="section-head">
        <p class="eyebrow">Subscription server</p>
      </div>

      <div class="fields fields-server">
        <label class="field">
          <span class="field-label">Domain</span>
          <input
            v-model="form.domain"
            class="field-input"
            :class="{ invalid: !!errors.domain }"
            type="text"
            inputmode="url"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            placeholder="sub.example.com"
            @blur="touched.domain = true"
          />
          <span class="reveal" :class="{ open: !!errors.domain }">
            <span class="field-hint" role="status">{{ hintText.domain }}</span>
          </span>
        </label>

        <label class="field">
          <span class="field-label">Port</span>
          <input
            v-model="form.port"
            class="field-input"
            :class="{ invalid: !!errors.port }"
            type="text"
            inputmode="numeric"
            autocomplete="off"
            spellcheck="false"
            placeholder="2096"
            @blur="touched.port = true"
          />
          <span class="reveal" :class="{ open: !!errors.port }">
            <span class="field-hint" role="status">{{ hintText.port }}</span>
          </span>
        </label>

        <label class="field field-salt">
          <span class="field-label">Salt</span>
          <span class="input-wrap">
            <input
              v-model="form.salt"
              class="field-input has-eye"
              :type="showSalt ? 'text' : 'password'"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              placeholder="The salt set at install"
              data-1p-ignore
              data-lpignore="true"
            />
            <button
              class="eye"
              type="button"
              :aria-label="showSalt ? 'Hide salt' : 'Show salt'"
              :title="showSalt ? 'Hide' : 'Show'"
              :aria-pressed="showSalt"
              @click="showSalt = !showSalt"
            >
              <svg v-if="showSalt" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M3 3l18 18" />
                <path d="M10.6 5.1A10.4 10.4 0 0 1 12 5c6 0 9.5 7 9.5 7a17 17 0 0 1-2.6 3.4M6.6 6.6A16.6 16.6 0 0 0 2.5 12S6 19 12 19a9.6 9.6 0 0 0 5.4-1.6" />
                <path d="M9.9 9.9a3 3 0 0 0 4.2 4.2" />
              </svg>
              <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M2.5 12S6 5 12 5s9.5 7 9.5 7-3.5 7-9.5 7S2.5 12 2.5 12z" />
                <circle cx="12" cy="12" r="3" />
              </svg>
            </button>
          </span>
        </label>
      </div>

      <div class="divider"></div>

      <div class="control-row">
        <div class="control-text">
          <p class="eyebrow">Mode</p>
          <p class="control-desc">
            {{ tailscale ? "Full profiles that also join a tailnet" : "The links every client subscribes to" }}
          </p>
        </div>
        <SegmentedControl v-model="form.mode" class="switch" :options="MODES" label="Mode" />
      </div>

      <!-- Everything Tailscale adds unfolds under the switch rather than
           appearing: the rows below it slide down instead of jumping. -->
      <div class="reveal" :class="{ open: tailscale }" :inert="!tailscale || undefined">
        <div class="reveal-inner">
          <div class="control-row ts-row">
            <div class="control-text">
              <p class="eyebrow">Role</p>
              <p class="control-desc">
                {{ form.role === "server" ? "The official Tailscale app runs beside the proxy" : "The proxy runs Tailscale itself" }}
              </p>
            </div>
            <SegmentedControl v-model="form.role" class="switch" :options="ROLES" label="Tailscale role" />
          </div>

          <div class="reveal" :class="{ open: client }" :inert="!client || undefined">
            <div class="reveal-inner">
              <div class="fields fields-client">
                <label class="field">
                  <span class="field-label">Hostname</span>
                  <input
                    v-model="form.hostname"
                    class="field-input"
                    :class="{ invalid: !!errors.hostname }"
                    type="text"
                    autocomplete="off"
                    autocapitalize="off"
                    spellcheck="false"
                    placeholder="my-phone"
                    @blur="touched.hostname = true"
                  />
                  <span class="reveal" :class="{ open: !!errors.hostname }">
                    <span class="field-hint" role="status">{{ hintText.hostname }}</span>
                  </span>
                </label>

                <label class="field">
                  <span class="field-label">Auth key</span>
                  <span class="input-wrap">
                    <input
                      v-model="form.authkey"
                      class="field-input has-eye"
                      :class="{ invalid: !!errors.authkey }"
                      :type="showAuthkey ? 'text' : 'password'"
                      autocomplete="off"
                      autocapitalize="off"
                      spellcheck="false"
                      placeholder="tskey-auth-…"
                      data-1p-ignore
                      data-lpignore="true"
                      @blur="touched.authkey = true"
                    />
                    <button
                      class="eye"
                      type="button"
                      :aria-label="showAuthkey ? 'Hide auth key' : 'Show auth key'"
                      :title="showAuthkey ? 'Hide' : 'Show'"
                      :aria-pressed="showAuthkey"
                      @click="showAuthkey = !showAuthkey"
                    >
                      <svg v-if="showAuthkey" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <path d="M3 3l18 18" />
                        <path d="M10.6 5.1A10.4 10.4 0 0 1 12 5c6 0 9.5 7 9.5 7a17 17 0 0 1-2.6 3.4M6.6 6.6A16.6 16.6 0 0 0 2.5 12S6 19 12 19a9.6 9.6 0 0 0 5.4-1.6" />
                        <path d="M9.9 9.9a3 3 0 0 0 4.2 4.2" />
                      </svg>
                      <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <path d="M2.5 12S6 5 12 5s9.5 7 9.5 7-3.5 7-9.5 7S2.5 12 2.5 12z" />
                        <circle cx="12" cy="12" r="3" />
                      </svg>
                    </button>
                  </span>
                  <span class="reveal" :class="{ open: !!errors.authkey }">
                    <span class="field-hint" role="status">{{ hintText.authkey }}</span>
                  </span>
                </label>
              </div>

              <div class="ts-note">
                <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
                  <circle cx="12" cy="12" r="9" stroke="currentColor" stroke-width="2" />
                  <path d="M12 11v5.5" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
                  <circle cx="12" cy="7.6" r="1.15" fill="currentColor" />
                </svg>
                <p>Needs sing-box 1.12 (iOS 1.13), mihomo 1.19.25, or Surge iOS 5.20 / Mac 6.7.</p>
              </div>
            </div>
          </div>
        </div>
      </div>
    </form>

    <section class="card subs-links">
      <!-- The token takes a line of its own under the title and the chip, so a
           phone shows all of it instead of the half the chip leaves room for. -->
      <div class="section-head links-head">
        <p class="eyebrow">Links</p>
        <span class="stamp-chip" :class="{ accent: tailscale }">
          <span class="stamp-value">{{ modeLabel }}</span>
        </span>
        <p v-if="token" class="links-token mono" :title="`Token ${token}`">{{ token }}</p>
      </div>

      <Transition name="swap" mode="out-in">
        <div v-if="links.length === 0" key="empty" class="links-empty">
          <span class="empty-icon" aria-hidden="true">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M10 13a5 5 0 0 0 7.1 0l3-3a5 5 0 0 0-7.1-7.1l-1.5 1.5" />
              <path d="M14 11a5 5 0 0 0-7.1 0l-3 3a5 5 0 0 0 7.1 7.1l1.5-1.5" />
            </svg>
          </span>
          <p class="empty-text">{{ waitingOn }}</p>
          <p class="empty-sub">Links are built in this browser. The salt never leaves it.</p>
        </div>

        <TransitionGroup v-else key="list" tag="ul" name="row" class="link-list" :class="{ pending: sealing }" :aria-busy="sealing" appear>
          <li v-for="({ link, base, token: linkToken, query }, i) in rows" :key="link.dir" class="link-row" :style="{ '--i': i }">
            <div class="link-main">
              <div class="link-head">
                <span class="link-label">{{ link.label }}</span>
                <span class="link-dir mono">{{ link.dir }}</span>
              </div>
              <p
                :key="flashes[link.dir] ?? 0"
                class="link-url mono"
                :class="{ flash: (flashes[link.dir] ?? 0) > 0 }"
              ><span class="u-base"><template v-for="(part, p) in base" :key="p">{{ part }}<wbr /></template></span><span class="u-token">{{ linkToken }}</span><span
                v-for="(piece, q) in query" :key="q" class="u-query">{{ piece }}</span></p>
            </div>
            <button
              class="copy-btn"
              :class="{ copied: copiedDir === link.dir }"
              type="button"
              :aria-label="`Copy the ${link.label} link`"
              :title="copiedDir === link.dir ? 'Copied' : 'Copy link'"
              :disabled="sealing"
              @click="copy(link)"
            >
              <svg class="icon-copy" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <rect x="9" y="9" width="12" height="12" rx="2" />
                <path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1" />
              </svg>
              <svg class="icon-check" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M5 12.5l4.5 4.5L19 7.5" />
              </svg>
            </button>
          </li>
        </TransitionGroup>
      </Transition>
    </section>

    <!-- The toast floats at the foot of the screen, out of every card, so a
         card's own entrance transform cannot become what it is positioned
         against. -->
    <Teleport to="body">
      <Transition name="toast">
        <div v-if="toast" class="subs-toast" :class="{ failed: !toast.ok }" role="status">
          <svg v-if="toast.ok" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <circle cx="12" cy="12" r="9" stroke="currentColor" stroke-width="2" />
            <path d="M8 12.4l2.7 2.7L16.2 9.5" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          <svg v-else viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <circle cx="12" cy="12" r="9" stroke="currentColor" stroke-width="2" />
            <path d="M12 7.5v5.5" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
            <circle cx="12" cy="16.4" r="1.15" fill="currentColor" />
          </svg>
          <span>{{ toast.text }}</span>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<style scoped>
.subs { display: grid; gap: 14px; }
.subs-links { animation-delay: 0.06s; }
.section-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; margin-bottom: 14px; }
.section-head .eyebrow { margin: 0; }

/* ── Fields ─────────────────────────────────────────────────────── */
.subs-form { container: subform / inline-size; }
.fields { display: grid; gap: 12px 14px; align-items: start; }
.fields-server { grid-template-columns: minmax(0, 1.5fr) minmax(0, 0.6fr) minmax(0, 1.5fr); }
.fields-client { grid-template-columns: minmax(0, 1fr) minmax(0, 1.6fr); padding-top: 14px; }
@container subform (max-width: 640px) {
  .fields-server { grid-template-columns: minmax(0, 1fr) 104px; }
  .fields-server .field-salt { grid-column: 1 / -1; }
  .fields-client { grid-template-columns: minmax(0, 1fr); }
}
.field { display: flex; flex-direction: column; min-width: 0; }
.field-label { margin-bottom: 6px; color: var(--text); font-size: 12.5px; font-weight: 700; }
.field-input {
  width: 100%; min-width: 0; height: 40px;
  border: 1px solid var(--line-strong); border-radius: var(--radius-sm);
  padding: 0 12px; background: var(--surface-2); color: var(--text);
  font: inherit; font-family: var(--font-mono); font-size: 13.5px;
  transition: border-color var(--dur) ease, background-color var(--dur) ease;
}
.field-input::placeholder { color: var(--faint); }
/* An outline rather than an animated shadow, the same focus the token gate
   draws: it arrives with the caret instead of fading in behind it. */
.field-input:focus { outline: 3px solid var(--accent-soft); outline-offset: 0; border-color: var(--accent); }
.field-input.invalid { border-color: color-mix(in srgb, var(--red) 55%, var(--line-strong)); }
.field-input.invalid:focus { outline-color: var(--danger-soft); }
.field-input.has-eye { padding-right: 42px; }
/* Phones zoom into any field set smaller than 16px when it takes focus. */
@media (pointer: coarse) {
  .field-input { height: 44px; font-size: 16px; }
}
.input-wrap { position: relative; display: block; }
.eye {
  position: absolute; top: 50%; right: 5px; transform: translateY(-50%);
  width: 32px; height: 32px; display: grid; place-items: center; padding: 0;
  border: none; border-radius: 8px; background: transparent; color: var(--muted); cursor: pointer;
  transition: color var(--dur) ease, background-color var(--dur) ease;
}
.eye:hover { color: var(--text); background: var(--hover); }
.eye svg { width: 17px; height: 17px; }
.field-hint { display: block; padding-top: 6px; color: var(--red); font-size: 12px; font-weight: 600; line-height: 1.35; }
/* Advice rather than an error: the value works, it is just weak. */

/* ── Unfolding regions ──────────────────────────────────────────── */
/* A region opens by growing its one grid row from nothing to its content's
   height, which animates to the real height without measuring anything. */
.reveal {
  display: grid; grid-template-rows: 0fr; opacity: 0;
  transition: grid-template-rows 0.3s var(--ease), opacity 0.2s ease;
}
.reveal.open { grid-template-rows: 1fr; opacity: 1; }
.reveal > * { min-height: 0; overflow: hidden; }
.reveal-inner { display: block; }

/* ── Mode rows ──────────────────────────────────────────────────── */
.divider { height: 1px; margin: 18px 0 16px; background: var(--line); }
.control-row { display: flex; align-items: center; justify-content: space-between; gap: 10px 18px; flex-wrap: wrap; }
.control-text { min-width: 0; }
.control-text .eyebrow { margin-bottom: 5px; }
.control-desc { margin: 0; color: var(--muted); font-size: 12.5px; font-weight: 600; }
.switch { min-width: 220px; }
.ts-row { padding-top: 16px; }
@container subform (max-width: 480px) {
  .control-row { flex-direction: column; align-items: stretch; }
  .switch { min-width: 0; width: 100%; }
}

.ts-note {
  display: flex; gap: 10px; margin-top: 16px; padding: 12px 14px;
  border: 1px solid var(--line); border-radius: var(--radius-sm); background: var(--surface-2);
  color: var(--muted); font-size: 12.5px; line-height: 1.5;
}
.ts-note svg { width: 16px; height: 16px; flex-shrink: 0; margin-top: 2px; color: var(--cyan); }
.ts-note p { margin: 0; min-width: 0; }

/* ── Links ──────────────────────────────────────────────────────── */
.links-head { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 0 12px; }
.links-token {
  grid-column: 1 / -1; margin: 6px 0 0; color: var(--faint); font-size: 11px; letter-spacing: 0.02em;
  overflow-wrap: anywhere;
}
.links-empty {
  display: flex; flex-direction: column; align-items: center; text-align: center;
  padding: 22px 12px 18px;
}
.empty-icon {
  width: 44px; height: 44px; display: grid; place-items: center;
  border-radius: 14px; background: var(--accent-soft); color: var(--accent);
}
.empty-icon svg { width: 20px; height: 20px; }
.empty-text { margin: 14px 0 0; color: var(--text); font-size: 13.5px; font-weight: 650; }
.empty-sub { margin: 4px 0 0; color: var(--faint); font-size: 12px; }

.link-list { position: relative; margin: 0; padding: 0; list-style: none; display: grid; gap: 8px; }
/* While a new auth key is sealed the rows still show the links before it,
   faded and not to be copied, until the new ones land a moment later. */
.link-list .link-main { transition: opacity 0.2s ease; }
.link-list.pending .link-main { opacity: 0.5; }
.copy-btn:disabled { cursor: default; opacity: 0.5; }
.link-row {
  display: flex; align-items: center; gap: 12px;
  padding: 12px 12px 12px 14px;
  border: 1px solid var(--line); border-radius: var(--radius-sm); background: var(--surface-2);
  transition: border-color var(--dur) ease;
}
.link-row:hover { border-color: var(--line-strong); }
.link-main { flex: 1; min-width: 0; }
.link-head { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
.link-label { color: var(--text-strong); font-size: 13.5px; font-weight: 750; white-space: nowrap; }
.link-dir {
  color: var(--faint); font-size: 10.5px; letter-spacing: 0.02em;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
/* The URL wraps anywhere rather than truncating: a link is copied whole, and
   the part that would be cut off is the query that makes it a Tailscale one. */
.link-url {
  margin: 5px -4px 0; padding: 1px 4px; border-radius: 5px;
  font-size: 12px; line-height: 1.55; overflow-wrap: anywhere;
}
.link-url.flash { animation: urlFlash 0.9s var(--ease); }
@keyframes urlFlash { from { background: var(--accent-soft); } to { background: transparent; } }
.u-base { color: var(--muted); }
.u-token { color: var(--text); }
/* Each parameter is one box: it moves to the next line whole when it does not
   fit, and only wraps inside itself when it is wider than the row. */
.u-query { display: inline-block; max-width: 100%; color: var(--accent); }

.copy-btn {
  position: relative; flex-shrink: 0;
  width: 36px; height: 36px; display: grid; place-items: center; padding: 0;
  border: 1px solid var(--line); border-radius: 10px; background: var(--surface-solid);
  color: var(--muted); cursor: pointer;
  transition: color var(--dur) ease, border-color var(--dur) ease, background-color var(--dur) ease, transform var(--dur) var(--ease);
}
.copy-btn:not(:disabled):hover { color: var(--accent); border-color: var(--accent-border); }
.copy-btn:not(:disabled):active { transform: scale(0.9); transition-duration: 60ms; }
.copy-btn.copied { color: var(--green); border-color: color-mix(in srgb, var(--green) 45%, transparent); background: var(--ok-soft); }
/* The two icons sit on the same spot and trade places: the copy glyph shrinks
   and fades as the check grows in, so the button morphs rather than blinks. */
.copy-btn svg {
  grid-area: 1 / 1; width: 16px; height: 16px;
  transition: opacity var(--dur) ease, transform 0.26s var(--ease);
}
.copy-btn .icon-check { opacity: 0; transform: scale(0.4) rotate(-20deg); }
.copy-btn.copied .icon-copy { opacity: 0; transform: scale(0.4); }
.copy-btn.copied .icon-check { opacity: 1; transform: none; }
@media (pointer: coarse) {
  .copy-btn { width: 42px; height: 42px; }
}

/* Rows come in one after another, and a row that goes (the share links, on
   switching to Tailscale) fades out of the flow while the rest close up. */
.row-enter-active { transition: opacity 0.3s ease, transform 0.36s var(--ease); transition-delay: calc(var(--i) * 45ms); }
.row-leave-active { position: absolute; left: 0; right: 0; transition: opacity 0.16s ease, transform 0.16s ease; }
.row-enter-from { opacity: 0; transform: translateY(8px); }
.row-leave-to { opacity: 0; transform: scale(0.98); }
.row-move { transition: transform 0.32s var(--ease); }

.swap-enter-active { transition: opacity 0.2s ease, transform 0.24s var(--ease); }
.swap-leave-active { transition: opacity 0.12s ease; }
.swap-enter-from { opacity: 0; transform: translateY(4px); }
.swap-leave-to { opacity: 0; }

@container app (max-width: 759px) {
  .subs { gap: 10px; }
  .link-row { padding: 10px 10px 10px 12px; gap: 10px; }
  .link-url { font-size: 11.5px; }
}

/* ── Toast ──────────────────────────────────────────────────────── */
.subs-toast {
  position: fixed; left: 50%; bottom: calc(24px + env(safe-area-inset-bottom)); z-index: 50;
  display: flex; align-items: center; gap: 10px;
  width: max-content; max-width: min(92vw, 440px); padding: 12px 16px;
  border-radius: 14px; border: 1px solid color-mix(in srgb, var(--green) 30%, transparent);
  background: var(--surface-solid); color: var(--text);
  font-size: 13px; font-weight: 650; line-height: 1.35;
  box-shadow: var(--shadow-hover);
  transform: translate(-50%, 0);
}
.subs-toast svg { width: 18px; height: 18px; flex-shrink: 0; color: var(--green); }
.subs-toast.failed { border-color: color-mix(in srgb, var(--red) 30%, transparent); color: var(--red); }
.subs-toast.failed svg { color: var(--red); }
.toast-enter-active { transition: opacity 0.24s ease, transform 0.32s var(--ease); }
.toast-leave-active { transition: opacity 0.3s ease, transform 0.3s ease; }
.toast-enter-from { opacity: 0; transform: translate(-50%, 14px); }
.toast-leave-to { opacity: 0; transform: translate(-50%, 8px); }

/* The global kill switch shortens every duration, but a delay is not a
   duration: the staggered rows and the second card would still wait. */
@media (prefers-reduced-motion: reduce) {
  .subs-links { animation-delay: 0s; }
  .row-enter-active { transition-delay: 0s; }
  .toast-enter-from, .toast-leave-to { transform: translate(-50%, 0); }
}
</style>
