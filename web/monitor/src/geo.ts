import { ref } from "vue";

// Geolocation is resolved in the browser, not on the node: the monitor service
// runs on a low-memory VPS and has no business shipping a location database or
// calling a third party on every render.
//
// The endpoint is fixed. It used to be editable from the page, which put a
// URL every viewer's browser would call into a field any viewer could rewrite —
// a dashboard-wide setting stored per browser, and one more place to get wrong.
const GEO_ENDPOINT = "https://ipwho.is/{ip}";

const CACHE_KEY = "singbox-deploy.monitor.geoPlaces";
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

function loadCache(): Map<string, Place> {
  const raw = readStored(CACHE_KEY);
  if (!raw) return new Map();
  try {
    const parsed = JSON.parse(raw) as Record<string, Place>;
    // A cache written by an older build held plain strings; drop those rather
    // than rendering an object's worth of undefined fields.
    return new Map(Object.entries(parsed).filter(([, v]) => v && typeof v === "object"));
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
// being looked up, and one mapped to an empty place could not be resolved.
export const locations = ref<Record<string, Place>>(Object.fromEntries(cache));

// Country and place are separate columns in the table, so they are resolved as
// separate fields rather than as one string the table has to take apart again.
export interface Place {
  country: string;
  // ISO-3166 alpha-2, which is what turns into a flag.
  code: string;
  city: string;
}

const EMPTY: Place = { country: "", code: "", city: "" };

// Different services spell the same fields differently; read whichever of the
// known spellings is present rather than committing to one provider's shape.
function placeFrom(body: any): Place {
  if (!body || body.success === false || body.status === "fail") return EMPTY;
  const country = String(body.country ?? body.country_name ?? body.countryName ?? "").trim();
  const code = String(body.country_code ?? body.countryCode ?? "").trim().toUpperCase();
  const region = String(body.region ?? body.regionName ?? body.region_name ?? body.state ?? "").trim();
  const city = String(body.city ?? "").trim();
  // Region often repeats the city name for municipalities; the narrower of the
  // two is the one worth the column.
  return { country, code, city: city || region };
}

async function lookup(ip: string): Promise<Place> {
  try {
    const res = await fetch(GEO_ENDPOINT.replaceAll("{ip}", encodeURIComponent(ip)), { cache: "no-store" });
    if (!res.ok) return EMPTY;
    return placeFrom(await res.json());
  } catch {
    return EMPTY;
  }
}

// A regional-indicator pair renders as the country's flag; anything that is not
// two letters yields nothing rather than a pair of stray glyphs.
export function flagFor(code: string): string {
  if (!/^[A-Z]{2}$/.test(code)) return "";
  return String.fromCodePoint(...[...code].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65));
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
      const place = await lookup(ip);
      pending.delete(ip);
      // An unresolved address is cached as empty so a service that is blocked
      // or out of quota is not re-queried on every refresh.
      cache.set(ip, place);
      locations.value = { ...locations.value, [ip]: place };
    }
  });
  await Promise.all(workers);
  persistCache();
}
