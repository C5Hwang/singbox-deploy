import type { Summary, HourlyPoint, ResourceHourlyPoint, TrafficRawPoint, ResourceRawPoint } from "./types";

const TOKEN_STORAGE_KEY = "singbox-deploy.monitor.token";

// UnauthorizedError separates "this dashboard wants a token" from an ordinary
// transport failure, so only the former opens the token prompt.
export class UnauthorizedError extends Error {
  constructor() {
    super("A monitor access token is required.");
    this.name = "UnauthorizedError";
  }
}

// Storage is unavailable in some privacy modes, and a dashboard that cannot
// remember the token is still usable — it just asks again on every reload.
function readStoredToken(): string {
  try {
    return localStorage.getItem(TOKEN_STORAGE_KEY) ?? "";
  } catch {
    return "";
  }
}

let accessToken = readStoredToken();
let unauthorizedHandler: (() => void) | null = null;

export function setAccessToken(token: string) {
  accessToken = token.trim();
  try {
    if (accessToken) localStorage.setItem(TOKEN_STORAGE_KEY, accessToken);
    else localStorage.removeItem(TOKEN_STORAGE_KEY);
  } catch {
    /* keep the token for this session only */
  }
}

export function hasStoredAccessToken(): boolean {
  return accessToken !== "";
}

// onUnauthorized lets the shell reopen the token prompt no matter which view
// made the request that was rejected.
export function onUnauthorized(handler: () => void) {
  unauthorizedHandler = handler;
}

async function getJSON(path: string): Promise<any> {
  const headers: Record<string, string> = {};
  if (accessToken) headers["Authorization"] = `Bearer ${accessToken}`;
  const res = await fetch(path, { cache: "no-store", headers });
  if (res.status === 401) {
    unauthorizedHandler?.();
    throw new UnauthorizedError();
  }
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

function sourceQuery(source?: string): string {
  return source ? `?source=${encodeURIComponent(source)}` : "";
}

export async function fetchSummary(): Promise<Summary> {
  return getJSON("api/summary");
}

export async function fetchTrafficTrend(source?: string): Promise<HourlyPoint[]> {
  const data = await getJSON(`api/traffic-trend${sourceQuery(source)}`);
  return data.trend ?? [];
}

export async function fetchTrafficRecent(source?: string): Promise<TrafficRawPoint[]> {
  const data = await getJSON(`api/traffic-recent${sourceQuery(source)}`);
  return data.points ?? [];
}

export async function fetchResourceTrend(source?: string): Promise<ResourceHourlyPoint[]> {
  const data = await getJSON(`api/resource-trend${sourceQuery(source)}`);
  return data.trend ?? [];
}

export async function fetchResourceRecent(source?: string): Promise<ResourceRawPoint[]> {
  const data = await getJSON(`api/resource-recent${sourceQuery(source)}`);
  return data.points ?? [];
}
