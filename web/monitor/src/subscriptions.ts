import { reactive, watch } from "vue";
import { md5Hex } from "./md5";

// The subscription page's form, and the links it builds. The state lives here
// rather than in the page because the page transition unmounts whatever page
// is left: a salt typed, a tab visited and the page come back to should still
// be there. Only the domain and the port are remembered across loads; the salt
// and the auth key are secrets and stay in this tab's memory, nowhere else.

export type SubscriptionMode = "standard" | "tailscale";
export type TailscaleRole = "server" | "client";

// The subscription server listens on a port of its own, not the dashboard's.
export const DEFAULT_PORT = "2096";

const DOMAIN_KEY = "singbox-deploy.monitor.subscribe.domain";
const PORT_KEY = "singbox-deploy.monitor.subscribe.port";

function loadStored(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function store(key: string, value: string) {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // The value still applies for this session.
  }
}

export const subscriptionForm = reactive({
  domain: loadStored(DOMAIN_KEY) ?? window.location.hostname,
  port: loadStored(PORT_KEY) ?? DEFAULT_PORT,
  salt: "",
  mode: "standard" as SubscriptionMode,
  role: "server" as TailscaleRole,
  hostname: "",
  authkey: "",
});

watch(
  () => subscriptionForm.domain,
  (value) => store(DOMAIN_KEY, value.trim()),
);
watch(
  () => subscriptionForm.port,
  (value) => store(PORT_KEY, value.trim()),
);

// The same rules the subscription service applies to the query, so a link
// this page offers is never one the server refuses.
const TAILSCALE_HOSTNAME = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/;
const TAILSCALE_AUTHKEY = /^tskey-[A-Za-z0-9_-]{1,200}$/;
const DNS_NAME = /^(?=.{1,253}$)(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)*[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/;
const IPV4 = /^(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)$/;

// A host as it goes into a URL: an IPv6 address in brackets, anything else as
// typed. Null when it is not a plausible host at all.
export function urlHost(raw: string): string | null {
  const value = raw.trim().replace(/\.$/, "");
  if (!value) return null;
  if (value.includes(":")) {
    const bare = value.replace(/^\[|\]$/g, "");
    try {
      return new URL(`https://[${bare}]/`).host;
    } catch {
      return null;
    }
  }
  // A name made only of digits and dots is an address, and has to be a whole
  // one: 10.0.0 or 300.1.1.1 would pass as a DNS name.
  if (/^[\d.]+$/.test(value)) return IPV4.test(value) ? value : null;
  return DNS_NAME.test(value) ? value.toLowerCase() : null;
}

// Each check says what is wrong with a field, or "" when nothing is. An empty
// field is not an error: it is just not filled in yet, which the page says
// once, calmly, in place of the links.
export function domainError(raw: string): string {
  const value = raw.trim();
  if (!value || urlHost(value)) return "";
  if (value.includes("://")) return "Just the domain — leave out https://";
  if (value.includes("/")) return "Just the domain, without a path";
  return "Not a valid domain or IP address";
}

export function portError(raw: string): string {
  const value = raw.trim();
  if (!value) return "";
  const port = /^\d{1,5}$/.test(value) ? Number(value) : NaN;
  return port >= 1 && port <= 65535 ? "" : "Use a port from 1 to 65535";
}

export function hostnameError(raw: string): string {
  const value = raw.trim();
  if (!value || TAILSCALE_HOSTNAME.test(value)) return "";
  return "Letters, digits and hyphens, up to 63, not starting or ending with a hyphen";
}

export function authkeyError(raw: string): string {
  const value = raw.trim();
  if (!value || TAILSCALE_AUTHKEY.test(value)) return "";
  if (!value.startsWith("tskey-")) return "An auth key starts with tskey-";
  return "Only letters, digits, - and _ after tskey-";
}

export interface SubscriptionLink {
  // The directory the file is served from, which is also the row's identity.
  dir: string;
  label: string;
  url: string;
}

// Every format the hub publishes, and which of them are full profiles: only a
// full profile carries the routing and DNS a Tailscale role has to change, so
// the share-link list has no Tailscale variant.
const FORMATS: { dir: string; label: string; profile: boolean }[] = [
  { dir: "default", label: "Share links (base64)", profile: false },
  { dir: "clashMetaProfiles", label: "Clash Meta", profile: true },
  { dir: "singboxProfiles", label: "sing-box", profile: true },
  { dir: "surgeProfiles", label: "Surge", profile: true },
];

export function subscriptionToken(salt: string): string {
  return md5Hex(salt.trim() + "\n");
}

// What still stands between the form and a set of links, or "" once the form
// is complete and valid. Errors on a field are shown under that field; this is
// the one line the empty state shows.
export function missingInput(form: typeof subscriptionForm): string {
  if (!form.domain.trim()) return "Enter the domain the subscription server answers on.";
  if (domainError(form.domain)) return "Check the domain: it is not a valid host yet.";
  if (!form.port.trim()) return "Enter the subscription port.";
  if (portError(form.port)) return "Check the port: it has to be 1 to 65535.";
  if (!form.salt.trim()) return "Enter the salt to generate the links.";
  if (form.mode === "tailscale" && form.role === "client") {
    if (!form.hostname.trim()) return "Enter the hostname this device joins the tailnet as.";
    if (hostnameError(form.hostname)) return "Check the hostname: it is not one Tailscale accepts.";
    if (!form.authkey.trim()) return "Enter a Tailscale auth key.";
    if (authkeyError(form.authkey)) return "Check the auth key: it does not look like a tskey-… key.";
  }
  return "";
}

// A Client link carries the auth key sealed, never as typed: sealedAuthkey is
// what encryptAuthKey made of the form's key, and without it there is no
// Client link to build.
export function buildLinks(form: typeof subscriptionForm, sealedAuthkey = ""): SubscriptionLink[] {
  if (missingInput(form)) return [];
  if (form.mode === "tailscale" && form.role === "client" && !sealedAuthkey) return [];
  const host = urlHost(form.domain)!;
  const port = Number(form.port.trim());
  const origin = port === 443 ? `https://${host}` : `https://${host}:${port}`;
  const token = subscriptionToken(form.salt);

  let query = "";
  if (form.mode === "tailscale") {
    query =
      form.role === "server"
        ? "?tailscale=server"
        : `?tailscale=client&hostname=${encodeURIComponent(form.hostname.trim())}` +
          `&authkey=${encodeURIComponent(sealedAuthkey)}`;
  }

  return FORMATS.filter((f) => !query || f.profile).map((f) => ({
    dir: f.dir,
    label: f.label,
    url: `${origin}/s/${f.dir}/${token}${query}`,
  }));
}

// Puts text on the clipboard. The async clipboard API exists only in a secure
// context, and a dashboard reached over plain HTTP by address is not one, so
// the old select-and-copy route is kept for that case.
export async function copyText(text: string): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard) {
    // A write that neither lands nor fails — a permission prompt nobody
    // answers — would leave the button with no feedback at all, so it gets a
    // second to settle before the old route is tried while the click still
    // counts as a user gesture.
    const settled = await Promise.race([
      navigator.clipboard.writeText(text).then(
        () => true,
        () => false,
      ),
      new Promise<false>((resolve) => window.setTimeout(() => resolve(false), 1000)),
    ]);
    if (settled) return true;
  }
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.style.position = "fixed";
  area.style.top = "0";
  area.style.left = "-9999px";
  area.style.opacity = "0";
  document.body.appendChild(area);
  const selection = document.getSelection();
  const previous = selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null;
  area.select();
  area.setSelectionRange(0, text.length);
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  }
  document.body.removeChild(area);
  if (previous && selection) {
    selection.removeAllRanges();
    selection.addRange(previous);
  }
  return ok;
}
