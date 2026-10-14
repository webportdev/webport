import { useCallback, useEffect, useRef, useState } from "react";

export type Page = "instances" | "routes" | "daemon";
export type InstanceTab = "services" | "urls" | "env" | "logs";
export type View = { page: Page } | { page: "instance"; id: string; tab: InstanceTab; service?: string };

const tabs: InstanceTab[] = ["services", "urls", "env", "logs"];

export function parseHash(hash: string): View {
  const [path = "", query = ""] = hash.replace(/^#\/?/, "").split("?");
  const parts = path.split("/").filter(Boolean).map(decodeURIComponent);
  if (parts[0] === "routes") return { page: "routes" };
  if (parts[0] === "daemon") return { page: "daemon" };
  if (parts[0] === "instances" && parts[1]) {
    const tab = tabs.includes(parts[2] as InstanceTab) ? (parts[2] as InstanceTab) : "services";
    const service = new URLSearchParams(query).get("service") ?? undefined;
    return { page: "instance", id: parts[1], tab, service };
  }
  return { page: "instances" };
}

export function formatHash(view: View): string {
  if (view.page !== "instance") return `#/${view.page}`;
  const service = view.service ? `?service=${encodeURIComponent(view.service)}` : "";
  return `#/instances/${encodeURIComponent(view.id)}/${view.tab}${service}`;
}

/** Keeps the view in the URL hash so reloads and Back keep your place. */
export function useHashView(): [View, (view: View, replace?: boolean) => void] {
  const [view, setView] = useState(() => parseHash(window.location.hash));
  useEffect(() => {
    const onChange = () => setView(parseHash(window.location.hash));
    window.addEventListener("hashchange", onChange);
    return () => window.removeEventListener("hashchange", onChange);
  }, []);
  const navigate = useCallback((next: View, replace = false) => {
    const hash = formatHash(next);
    if (hash !== window.location.hash) {
      if (replace) window.history.replaceState(null, "", hash);
      else window.history.pushState(null, "", hash);
    }
    setView(next);
  }, []);
  return [view, navigate];
}

/** Runs callback now and every interval while enabled and the tab is visible. */
export function useInterval(callback: () => void, interval: number, enabled: boolean, deps: unknown[] = []) {
  const saved = useRef(callback);
  saved.current = callback;
  useEffect(() => {
    if (!enabled) return;
    saved.current();
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") saved.current();
    }, interval);
    const onVisible = () => document.visibilityState === "visible" && saved.current();
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [interval, enabled, ...deps]);
}

export async function copyText(value: string): Promise<void> {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const area = document.createElement("textarea");
  area.value = value;
  area.setAttribute("readonly", "");
  area.style.position = "fixed";
  area.style.opacity = "0";
  document.body.appendChild(area);
  area.select();
  const ok = document.execCommand("copy");
  area.remove();
  if (!ok) throw new Error("The browser blocked clipboard access");
}

export type ThemeChoice = "system" | "light" | "dark";

function readTheme(): ThemeChoice {
  try {
    const value = localStorage.getItem("webport-theme");
    if (value === "light" || value === "dark") return value;
  } catch {
    // Storage may be unavailable; fall back to the system theme.
  }
  return "system";
}

export function useTheme(): [ThemeChoice, () => void] {
  const [theme, setTheme] = useState<ThemeChoice>(readTheme);
  useEffect(() => {
    const root = document.documentElement;
    if (theme === "system") root.removeAttribute("data-theme");
    else root.setAttribute("data-theme", theme);
    try {
      if (theme === "system") localStorage.removeItem("webport-theme");
      else localStorage.setItem("webport-theme", theme);
    } catch {
      // Ignore unavailable storage.
    }
  }, [theme]);
  const cycle = useCallback(() => setTheme((t) => (t === "system" ? "light" : t === "light" ? "dark" : "system")), []);
  return [theme, cycle];
}
