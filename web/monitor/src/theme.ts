import { ref } from "vue";

// The dashboard's colour scheme. It follows the system until the viewer picks
// one in the top bar, and the pick is remembered per browser the same way the
// timezone is. The attribute is what the stylesheet keys off; the ref is what
// the charts key off, because a canvas cannot read a CSS variable on its own.
export type Theme = "light" | "dark";

const STORAGE_KEY = "singbox-deploy.monitor.theme";
const media = window.matchMedia("(prefers-color-scheme: dark)");

function stored(): Theme | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw === "light" || raw === "dark" ? raw : null;
  } catch {
    return null;
  }
}

function system(): Theme {
  return media.matches ? "dark" : "light";
}

function apply(value: Theme) {
  document.documentElement.setAttribute("data-theme", value);
}

export const theme = ref<Theme>(stored() ?? system());
export const themeOverridden = ref(stored() !== null);
apply(theme.value);

export function setTheme(value: Theme) {
  theme.value = value;
  themeOverridden.value = true;
  apply(value);
  try {
    localStorage.setItem(STORAGE_KEY, value);
  } catch {
    /* the pick still applies for this session */
  }
}

export function toggleTheme() {
  setTheme(theme.value === "dark" ? "light" : "dark");
}

// A viewer who never chose keeps following the system when it switches.
media.addEventListener("change", () => {
  if (themeOverridden.value) return;
  theme.value = system();
  apply(theme.value);
});

// cssVar reads a token off the root element, resolved for the current theme.
// The charts are drawn on a canvas and take literal colours, so this is how
// they get the same ink and rules the DOM around them is using.
export function cssVar(name: string, fallback = ""): string {
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return value || fallback;
}
