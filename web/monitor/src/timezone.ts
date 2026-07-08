import { ref } from "vue";

const STORAGE_KEY = "monitor.tzOffsetMinutes";
const MIN_OFFSET = -12 * 60;
const MAX_OFFSET = 14 * 60;

// getTimezoneOffset() is minutes behind UTC (GMT+8 -> -480); invert so
// positive offsets mean east of Greenwich.
export function browserOffsetMinutes(): number {
  return -new Date().getTimezoneOffset();
}

function loadStored(): number | null {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (raw === null) return null;
    const value = Number(raw);
    if (!Number.isFinite(value) || value < MIN_OFFSET || value > MAX_OFFSET) return null;
    return value;
  } catch {
    return null;
  }
}

const stored = loadStored();

// Display offset for every timestamp on the page. Defaults to the visitor's
// own timezone; an explicit pick from the topbar menu overrides it.
export const tzOffsetMinutes = ref(stored ?? browserOffsetMinutes());
export const tzOverridden = ref(stored !== null);

export function setTzOffset(minutes: number): void {
  tzOffsetMinutes.value = minutes;
  tzOverridden.value = true;
  try {
    window.localStorage.setItem(STORAGE_KEY, String(minutes));
  } catch {
    // Selection still applies for this session.
  }
}

export function clearTzOverride(): void {
  tzOffsetMinutes.value = browserOffsetMinutes();
  tzOverridden.value = false;
  try {
    window.localStorage.removeItem(STORAGE_KEY);
  } catch {
    // Ignore; nothing was persisted.
  }
}

export function gmtLabel(minutes: number): string {
  if (minutes === 0) return "GMT";
  const sign = minutes < 0 ? "-" : "+";
  const abs = Math.abs(minutes);
  const hours = Math.floor(abs / 60);
  const rest = abs % 60;
  return rest === 0
    ? `GMT${sign}${hours}`
    : `GMT${sign}${hours}:${String(rest).padStart(2, "0")}`;
}

// Shift a timestamp so that formatting the result with timeZone "UTC" renders
// wall-clock time in the selected offset. Works for any offset, including the
// half-hour ones a browser default may produce.
export function shiftToTz(value: number | string | Date): Date {
  return new Date(new Date(value).getTime() + tzOffsetMinutes.value * 60_000);
}

export interface TzOption {
  minutes: number;
  label: string;
}

export function tzOptions(): TzOption[] {
  const offsets: number[] = [];
  for (let h = MIN_OFFSET; h <= MAX_OFFSET; h += 60) offsets.push(h);
  const detected = browserOffsetMinutes();
  if (detected % 60 !== 0 && !offsets.includes(detected)) offsets.push(detected);
  offsets.sort((a, b) => a - b);
  return offsets.map((minutes) => ({ minutes, label: gmtLabel(minutes) }));
}
