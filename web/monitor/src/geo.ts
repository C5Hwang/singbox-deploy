import { ref } from "vue";

// Geolocation is resolved in the browser, not on the node: the monitor service
// runs on a low-memory VPS and has no business shipping a location database or
// calling a third party on every render.
export const DEFAULT_GEO_ENDPOINT = "https://ipwho.is/{ip}";

const ENDPOINT_KEY = "singbox-deploy.monitor.geoEndpoint";
const CACHE_KEY = "singbox-deploy.monitor.geoCache";
// Well past the thirty addresses a page shows, and small enough that the cache
// stays inside the few-hundred-kilobyte budget browsers give one origin.
const CACHE_LIMIT = 500;
// Public lookup services rate-limit per client, so a page-load burst of thirty
// addresses is spread over a few round trips.
const CONCURRENCY = 4;

function readStored(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStored(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* the lookup still works, it just will not be remembered */
  }
}

// The endpoint is a template so an operator can point the dashboard at a
// service of their own — a self-hosted resolver, or a paid plan with a key —
// without a rebuild.
export const geoEndpoint = ref(readStored(ENDPOINT_KEY) ?? DEFAULT_GEO_ENDPOINT);

export function setGeoEndpoint(endpoint: string) {
  const value = endpoint.trim() || DEFAULT_GEO_ENDPOINT;
  geoEndpoint.value = value;
  writeStored(ENDPOINT_KEY, value);
  // Cached labels came from the previous service and may be worded differently.
  cache.clear();
  writeStored(CACHE_KEY, "{}");
}

function loadCache(): Map<string, string> {
  const raw = readStored(CACHE_KEY);
  if (!raw) return new Map();
  try {
    const parsed = JSON.parse(raw) as Record<string, string>;
    return new Map(Object.entries(parsed));
  } catch {
    return new Map();
  }
}

const cache = loadCache();

function persistCache() {
  // Map iteration is insertion-ordered, so dropping from the front evicts the
  // addresses looked up longest ago.
  while (cache.size > CACHE_LIMIT) {
    const oldest = cache.keys().next();
    if (oldest.done) break;
    cache.delete(oldest.value);
  }
  writeStored(CACHE_KEY, JSON.stringify(Object.fromEntries(cache)));
}

// locations is what the table renders from: an address missing from it is still
// being looked up, and one mapped to "" could not be resolved.
export const locations = ref<Record<string, string>>(Object.fromEntries(cache));

// Different services spell the same fields differently; read whichever of the
// known spellings is present rather than committing to one provider's shape.
function labelFrom(body: any): string {
  if (!body || body.success === false || body.status === "fail") return "";
  const country = body.country ?? body.country_name ?? body.countryName ?? "";
  const region = body.region ?? body.regionName ?? body.region_name ?? body.state ?? "";
  const city = body.city ?? "";
  const parts = [country, region, city].map((p: unknown) => String(p ?? "").trim()).filter(Boolean);
  // Region often repeats the city name for municipalities; showing it twice
  // wastes a column that is already narrow.
  return parts.filter((part, i) => parts.indexOf(part) === i).join(" · ");
}

async function lookup(ip: string): Promise<string> {
  try {
    const url = geoEndpoint.value.includes("{ip}")
      ? geoEndpoint.value.replaceAll("{ip}", encodeURIComponent(ip))
      : `${geoEndpoint.value.replace(/\/$/, "")}/${encodeURIComponent(ip)}`;
    const res = await fetch(url, { cache: "no-store" });
    if (!res.ok) return "";
    return labelFrom(await res.json());
  } catch {
    return "";
  }
}

const pending = new Set<string>();

// resolveLocations fills in whatever is not already known. It is safe to call
// on every render: cached and in-flight addresses are skipped.
export async function resolveLocations(ips: string[]) {
  const missing = ips.filter((ip) => !cache.has(ip) && !pending.has(ip));
  if (missing.length === 0) return;
  missing.forEach((ip) => pending.add(ip));

  const queue = [...missing];
  const workers = Array.from({ length: Math.min(CONCURRENCY, queue.length) }, async () => {
    for (let ip = queue.shift(); ip !== undefined; ip = queue.shift()) {
      const label = await lookup(ip);
      pending.delete(ip);
      // An unresolved address is cached as empty so a service that is blocked
      // or out of quota is not re-queried on every refresh.
      cache.set(ip, label);
      locations.value = { ...locations.value, [ip]: label };
    }
  });
  await Promise.all(workers);
  persistCache();
}
