import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, UnauthorizedError } from "./api";
import { HelpModal } from "./components/help";
import { Badge, Button, Empty, Icon, Modal, StatusDot, Toasts, URLText, type Toast } from "./components/ui";
import { copyText, useHashView, useInterval, type InstanceTab, type ThemeChoice, type View } from "./hooks";
import { instanceTone, readyCount, serviceNames, serviceTone } from "./logic";
import { LogsView } from "./LogsView";
import {
  daemonRows,
  envRows,
  instanceRows,
  routeRows,
  serviceRows,
  urlListRows,
  type Expanded,
  type Row,
  type RowActions,
} from "./rows";
import type { Inspection, Instance, Operation, Snapshot } from "./types";

const REFRESH_MS = 2000;

interface Props {
  theme: ThemeChoice;
  cycleTheme: () => void;
  onSignedOut: () => void;
}

const emptyText: Record<string, [string, string?]> = {
  instances: ["No instances", "Start one with webport dev -d, or webport dev -- npm run dev."],
  routes: ["No routes", "Start a server with webport dev."],
  daemon: ["Daemon unavailable", "Run webport doctor."],
  services: ["No services in this instance"],
  urls: ["No URLs yet", "Try another service."],
  env: ["No environment values available"],
};

const tabLabels: [InstanceTab, string, string][] = [
  ["services", "Services", "s"],
  ["urls", "URLs", "u"],
  ["env", "Environment", "e"],
  ["logs", "Logs", "l"],
];

export function Dashboard({ theme, cycleTheme, onSignedOut }: Props) {
  const [view, navigate] = useHashView();
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [fetchError, setFetchError] = useState("");
  const [paused, setPaused] = useState(false);
  const [fetching, setFetching] = useState(false);
  const [filter, setFilter] = useState("");
  const [selection, setSelection] = useState<Record<string, string>>({});
  const [sensitive, setSensitive] = useState(false);
  const [inherited, setInherited] = useState(false);
  const [inspection, setInspection] = useState<{ key: string; value?: Inspection; error?: string } | null>(null);
  const [logs, setLogs] = useState<{ key: string; lines: string[]; error?: string } | null>(null);
  const [confirm, setConfirm] = useState<{ operation: Operation; instance: Instance } | null>(null);
  const [busy, setBusy] = useState(false);
  const [help, setHelp] = useState(false);
  const [expanded, setExpanded] = useState<Expanded | null>(null);
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [now, setNow] = useState(() => Date.now());
  const searchRef = useRef<HTMLInputElement>(null);
  const toastID = useRef(0);
  const snapshotInFlight = useRef(false);

  const toast = useCallback((message: string, tone: Toast["tone"] = "info") => {
    const id = ++toastID.current;
    setToasts((items) => [...items.slice(-3), { id, message, tone }]);
    window.setTimeout(() => setToasts((items) => items.filter((item) => item.id !== id)), tone === "error" ? 6000 : 3500);
  }, []);

  const handleError = useCallback(
    (error: unknown, prefix = "") => {
      if (error instanceof UnauthorizedError) {
        onSignedOut();
        return;
      }
      if (error instanceof DOMException && error.name === "AbortError") return;
      toast(prefix + (error instanceof Error ? error.message : String(error)), "error");
    },
    [onSignedOut, toast],
  );

  const refresh = useCallback(async () => {
    if (snapshotInFlight.current) return;
    snapshotInFlight.current = true;
    setFetching(true);
    try {
      const next = await api.snapshot();
      setSnapshot(next);
      setFetchError("");
      setNow(Date.now());
    } catch (error) {
      if (error instanceof UnauthorizedError) onSignedOut();
      else setFetchError(error instanceof Error ? error.message : String(error));
    } finally {
      snapshotInFlight.current = false;
      setFetching(false);
    }
  }, [onSignedOut]);

  useInterval(() => void refresh(), REFRESH_MS, !paused);

  const instance = view.page === "instance" ? snapshot?.instances.find((item) => item.session_id === view.id) : undefined;
  const services = serviceNames(instance);
  const service = view.page === "instance" ? (view.service && services.includes(view.service) ? view.service : services[0] ?? "") : "";

  // Leave an instance view when its instance stops.
  useEffect(() => {
    if (view.page === "instance" && snapshot && !instance) {
      navigate({ page: "instances" }, true);
      toast("Instance stopped. Select another instance.");
    }
  }, [view, snapshot, instance, navigate, toast]);

  // Searches belong to one list.
  const viewKey = view.page === "instance" ? `${view.id}/${view.tab}/${service}` : view.page;
  useEffect(() => setFilter(""), [viewKey]);

  const inspectionKey = view.page === "instance" ? `${view.id}/${sensitive}/${inherited}` : "";
  const loadInspection = useCallback(async () => {
    if (view.page !== "instance" || view.tab !== "env") return;
    const key = inspectionKey;
    try {
      const value = await api.inspect(view.id, sensitive, inherited);
      setInspection({ key, value });
    } catch (error) {
      if (error instanceof UnauthorizedError) onSignedOut();
      else setInspection({ key, error: error instanceof Error ? error.message : String(error) });
    }
  }, [view, sensitive, inherited, inspectionKey, onSignedOut]);
  useInterval(() => void loadInspection(), REFRESH_MS, view.page === "instance" && view.tab === "env" && !paused, [inspectionKey]);
  useEffect(() => {
    if (paused) void loadInspection();
    // Toggling visibility refreshes immediately even while paused.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inspectionKey]);

  const logsKey = view.page === "instance" && view.tab === "logs" ? `${view.id}/${service}` : "";
  const loadLogs = useCallback(async () => {
    if (!logsKey || view.page !== "instance" || !service) return;
    try {
      const result = await api.logs(view.id, service);
      setLogs({ key: logsKey, lines: result.lines, error: result.error });
    } catch (error) {
      if (error instanceof UnauthorizedError) onSignedOut();
      else setLogs({ key: logsKey, lines: [], error: error instanceof Error ? error.message : String(error) });
    }
  }, [logsKey, view, service, onSignedOut]);
  useInterval(() => void loadLogs(), REFRESH_MS, Boolean(logsKey) && !paused, [logsKey]);

  const copyURL = useCallback(
    async (url: string) => {
      try {
        await copyText(url);
        toast("Copied URL");
      } catch (error) {
        handleError(error, "Copy failed: ");
      }
    },
    [toast, handleError],
  );

  // Cleartext is fetched only for this clipboard operation; it never enters
  // page state, so copying does not change what is displayed.
  const copyEnv = useCallback(
    async (name: string, entry: boolean) => {
      if (view.page !== "instance") return;
      try {
        const { value } = await api.envValue(view.id, service, name, inherited);
        await copyText(entry ? `${name}=${value}` : value);
        toast(entry ? `Copied ${name}=…` : `Copied ${name}`);
      } catch (error) {
        handleError(error, "Copy failed: ");
      }
    },
    [view, service, inherited, toast, handleError],
  );

  const actions: RowActions = useMemo(
    () => ({
      openInstance: (id) => {
        const target = snapshot?.instances.find((item) => item.session_id === id);
        navigate({ page: "instance", id, tab: "services", service: serviceNames(target)[0] });
      },
      openService: (name, tab = "urls") => view.page === "instance" && navigate({ ...view, tab, service: name }),
      control: (operation, target) => !busy && setConfirm({ operation, instance: target }),
      copyURL,
      copyEnv,
      expand: setExpanded,
    }),
    [snapshot, navigate, view, busy, copyURL, copyEnv],
  );

  const currentInspection = inspection?.key === inspectionKey ? inspection : undefined;
  const allRows: Row[] = useMemo(() => {
    if (!snapshot) return [];
    switch (view.page) {
      case "instances":
        return instanceRows(snapshot, actions, now);
      case "routes":
        return routeRows(snapshot, actions, now);
      case "daemon":
        return daemonRows(snapshot);
      case "instance":
        if (!instance) return [];
        if (view.tab === "services") return serviceRows(instance, actions);
        if (view.tab === "urls") return urlListRows(instance, service, actions);
        if (view.tab === "env") return envRows(currentInspection?.value, service, actions);
        return [];
    }
  }, [snapshot, view, instance, service, currentInspection, actions, now]);

  const rows = useMemo(() => {
    if (!filter) return allRows;
    const needle = filter.toLowerCase();
    return allRows.filter((row) => row.search.toLowerCase().includes(needle));
  }, [allRows, filter]);

  // Selections are kept by ID, so they survive refreshes and reordering.
  const selectedID = rows.some((row) => row.id === selection[viewKey]) ? selection[viewKey] : rows[0]?.id;
  const selectedIndex = rows.findIndex((row) => row.id === selectedID);
  const selected = rows[selectedIndex];
  const select = useCallback((id: string) => setSelection((items) => ({ ...items, [viewKey]: id })), [viewKey]);

  useEffect(() => {
    if (selectedID) document.getElementById(`row-${CSS.escape(selectedID)}`)?.scrollIntoView({ block: "nearest" });
  }, [selectedID]);

  const runControl = useCallback(async () => {
    if (!confirm) return;
    const { operation, instance: target } = confirm;
    setConfirm(null);
    setBusy(true);
    try {
      const result = await api.control(target.session_id, operation);
      toast(result.message);
    } catch (error) {
      handleError(error, `${operation} failed: `);
    } finally {
      setBusy(false);
      void refresh();
    }
  }, [confirm, toast, handleError, refresh]);

  const signOut = useCallback(async () => {
    try {
      await api.logout();
    } finally {
      onSignedOut();
    }
  }, [onSignedOut]);

  const switchService = useCallback(
    (delta: number) => {
      if (view.page !== "instance" || services.length === 0 || view.tab === "services") return;
      const index = Math.max(0, services.indexOf(service));
      navigate({ ...view, service: services[(index + delta + services.length) % services.length] }, true);
    },
    [view, services, service, navigate],
  );

  const openTab = useCallback(
    (tab: InstanceTab) => {
      if (view.page !== "instance") return;
      // Leaving Services keeps the selected service, as in the terminal dashboard.
      const next = view.tab === "services" && selectedID && services.includes(selectedID) ? selectedID : service;
      navigate({ ...view, tab, service: next }, true);
    },
    [view, selectedID, services, service, navigate],
  );

  const controlTarget = view.page === "instance" ? instance : view.page === "instances" ? snapshot?.instances.find((item) => item.session_id === selectedID) : undefined;

  // One keyboard handler mirrors the terminal dashboard's hotkeys.
  const keyHandler = useRef<(event: KeyboardEvent) => void>(() => {});
  keyHandler.current = (event: KeyboardEvent) => {
    if (event.ctrlKey || event.metaKey || event.altKey) return;
    const target = event.target as HTMLElement;
    const typing = target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable;
    if (confirm) {
      if (event.key === "y" || event.key === "Enter") {
        event.preventDefault();
        void runControl();
      } else if (event.key === "n" || event.key === "Escape") setConfirm(null);
      return;
    }
    if (help || expanded) {
      if (event.key === "Escape" || event.key === "?" || event.key === "q") {
        setHelp(false);
        setExpanded(null);
      }
      return;
    }
    if (typing) {
      if (event.key === "Escape" && target === searchRef.current) {
        setFilter("");
        target.blur();
      } else if (event.key === "Enter" && target === searchRef.current) target.blur();
      else if ((event.key === "ArrowDown" || event.key === "ArrowUp") && target === searchRef.current) {
        event.preventDefault();
        target.blur();
      } else return;
      return;
    }
    const move = (index: number) => {
      const row = rows[Math.max(0, Math.min(rows.length - 1, index))];
      if (row) select(row.id);
    };
    const key = event.key;
    let handled = true;
    switch (key) {
      case "ArrowDown":
      case "j":
        move(selectedIndex + 1);
        break;
      case "ArrowUp":
      case "k":
        move(selectedIndex - 1);
        break;
      case "PageDown":
        move(selectedIndex + 10);
        break;
      case "PageUp":
        move(selectedIndex - 10);
        break;
      case "Home":
      case "g":
        move(0);
        break;
      case "End":
      case "G":
        move(rows.length - 1);
        break;
      case "1":
        navigate({ page: "instances" });
        break;
      case "2":
        navigate({ page: "routes" });
        break;
      case "3":
        navigate({ page: "daemon" });
        break;
      case "/":
        searchRef.current?.focus();
        break;
      case "?":
        setHelp(true);
        break;
      case "p":
        setPaused((value) => !value);
        break;
      case "Enter":
        if (selected?.activate) selected.activate();
        else if (selected) setExpanded(selected.expand);
        break;
      case "d":
        if (selected) setExpanded(selected.expand);
        break;
      case "Escape":
      case "Backspace":
        if (filter) setFilter("");
        else if (view.page === "instance" && view.tab !== "services") navigate({ ...view, tab: "services" }, true);
        else if (view.page !== "instances") navigate({ page: "instances" });
        else handled = false;
        break;
      case "s":
      case "u":
      case "e":
      case "l":
        if (view.page === "instance") openTab(({ s: "services", u: "urls", e: "env", l: "logs" } as const)[key]);
        else handled = false;
        break;
      case "[":
        switchService(-1);
        break;
      case "]":
        switchService(1);
        break;
      case "v":
        if (view.page === "instance" && view.tab === "env") setSensitive((value) => !value);
        break;
      case "i":
        if (view.page === "instance" && view.tab === "env") setInherited((value) => !value);
        break;
      case "r":
      case "x":
        if (controlTarget && !busy) setConfirm({ operation: key === "r" ? "restart" : "stop", instance: controlTarget });
        break;
      case "o":
        if (selected?.url) window.open(selected.url, "_blank", "noopener,noreferrer");
        else toast("No URL available for this selection");
        break;
      case "c":
      case "y":
      case "Y":
        if (selected?.copy) void selected.copy(key === "Y");
        else toast("Select a URL or environment value to copy");
        break;
      default:
        handled = false;
    }
    if (handled) event.preventDefault();
  };
  useEffect(() => {
    const listener = (event: KeyboardEvent) => keyHandler.current(event);
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, []);

  const topPage = view.page === "instance" ? "instances" : view.page;
  const liveLabel = paused ? "Paused" : fetching && !snapshot ? "Connecting…" : "Live";
  const updated = snapshot ? new Date(snapshot.generated_at).toLocaleTimeString() : "";
  const banner = fetchError || snapshot?.error;
  const listTitle = view.page === "instance" ? tabLabels.find(([tab]) => tab === view.tab)![1] : { instances: "Instances", routes: "Routes", daemon: "Daemon" }[view.page];
  const empty = emptyText[view.page === "instance" ? view.tab : view.page] ?? ["Nothing here"];

  return (
    <div className="app">
      <header className="topbar">
        <a className="brand" href="#/instances" aria-label="Webport home">
          <span className="brand-mark" aria-hidden="true">
            ◆
          </span>
          <span className="brand-name">webport</span>
        </a>
        <nav className="nav" aria-label="Sections">
          {(
            [
              ["instances", "Instances", snapshot?.instances.length, "1"],
              ["routes", "Routes", snapshot?.routes.length, "2"],
              ["daemon", "Daemon", undefined, "3"],
            ] as const
          ).map(([page, label, count, key]) => (
            <a key={page} href={`#/${page}`} className={`nav-item${topPage === page ? " active" : ""}`} aria-current={topPage === page ? "page" : undefined} title={`${label} (${key})`}>
              {label}
              {count !== undefined && <span className="count">{count}</span>}
            </a>
          ))}
        </nav>
        <div className="topbar-actions">
          <button className={`live${paused ? " live-paused" : ""}`} onClick={() => setPaused((value) => !value)} title={paused ? "Resume automatic refresh (p)" : "Pause automatic refresh (p)"}>
            <StatusDot tone={paused ? "warn" : fetchError ? "bad" : "good"} pulse={!paused && !fetchError} />
            <span>{liveLabel}</span>
            {updated && <span className="live-time">{updated}</span>}
            <Icon name={paused ? "play" : "pause"} size={14} />
          </button>
          <Button icon="refresh" variant="ghost" title="Refresh now" onClick={() => void refresh()} />
          <Button icon="keyboard" variant="ghost" title="Keyboard shortcuts (?)" onClick={() => setHelp(true)} />
          <Button icon={theme === "dark" ? "moon" : theme === "light" ? "sun" : "monitor"} variant="ghost" title={`Theme: ${theme}`} onClick={cycleTheme} />
          <Button icon="logout" variant="ghost" title="Sign out" onClick={() => void signOut()} />
        </div>
      </header>

      {banner && (
        <div className="banner" role="alert">
          {banner}
        </div>
      )}

      <main className="main">
        {view.page === "instance" && instance && (
          <section className="instance-head">
            <div className="crumbs">
              <a href="#/instances" className="crumb-back">
                <Icon name="back" /> Instances
              </a>
            </div>
            <div className="instance-title">
              <div>
                <h1>{instance.name}</h1>
                <div className="instance-meta">
                  <span className="chip">{instance.profile}</span>
                  <Badge tone={instanceTone(instance)}>
                    <StatusDot tone={instanceTone(instance)} /> {readyCount(instance).ready}/{readyCount(instance).total} ready
                  </Badge>
                  {instance.url && <URLText url={instance.url} className="small" />}
                </div>
              </div>
              <div className="detail-actions">
                <Button icon="restart" disabled={busy} onClick={() => setConfirm({ operation: "restart", instance })} title="Restart instance (r)">
                  Restart
                </Button>
                <Button icon="stop" variant="danger" disabled={busy} onClick={() => setConfirm({ operation: "stop", instance })} title="Stop instance (x)">
                  Stop
                </Button>
              </div>
            </div>
            <div className="tabs" role="tablist">
              {tabLabels.map(([tab, label, key]) => (
                <button key={tab} role="tab" aria-selected={view.tab === tab} className={`tab${view.tab === tab ? " active" : ""}`} onClick={() => openTab(tab)} title={`${label} (${key})`}>
                  {label}
                </button>
              ))}
            </div>
          </section>
        )}

        <section className="toolbar">
          {view.page !== "instance" && <h1 className="page-title">{listTitle}</h1>}
          {view.page === "instance" && view.tab !== "services" && services.length > 0 && (
            <div className="segmented" role="group" aria-label="Service">
              {services.map((name) => (
                <button key={name} className={`segment${name === service ? " active" : ""}`} onClick={() => navigate({ ...view, service: name }, true)}>
                  <StatusDot tone={serviceTone(instance!.services[name]!)} /> {name}
                </button>
              ))}
            </div>
          )}
          <div className="toolbar-spacer" />
          {view.page === "instance" && view.tab === "env" && (
            <div className="toggles">
              <Button small icon={sensitive ? "eye" : "eyeOff"} pressed={sensitive} onClick={() => setSensitive((value) => !value)} title="Reveal or hide sensitive values (v)">
                {sensitive ? "Sensitive shown" : "Sensitive hidden"}
              </Button>
              <Button small icon="layers" pressed={inherited} onClick={() => setInherited((value) => !value)} title="Include or exclude inherited environment (i)">
                {inherited ? "Inherited included" : "Managed only"}
              </Button>
            </div>
          )}
          <label className="search">
            <Icon name="search" />
            <input ref={searchRef} type="search" placeholder={`Search ${listTitle.toLowerCase()}`} value={filter} onChange={(event) => setFilter(event.target.value)} aria-label="Search this list" />
            {!filter && <kbd className="kbd">/</kbd>}
          </label>
        </section>

        {view.page === "instance" && view.tab === "logs" ? (
          <LogsView
            lines={logs?.key === logsKey ? logs.lines : undefined}
            error={logs?.key === logsKey ? logs.error : undefined}
            loading={logs?.key !== logsKey}
            filter={filter}
            onExpand={(line) => setExpanded({ title: `${service} log line`, text: line })}
          />
        ) : (
          <div className="panes">
            <section className="list-pane" aria-label={listTitle}>
              <div className="list-head">
                <span>{listTitle}</span>
                <span className="muted">{rows.length === allRows.length ? rows.length : `${rows.length} of ${allRows.length}`}</span>
              </div>
              {view.page === "daemon" && snapshot && (
                <div className="self-card">
                  <span className="field-label">This dashboard</span>
                  {snapshot.webui.url ? <URLText url={snapshot.webui.url} /> : <span className="muted">Not published{snapshot.webui.route_error ? `: ${snapshot.webui.route_error}` : ""}</span>}
                </div>
              )}
              {!snapshot ? (
                <div className="skeleton-list" aria-label="Loading">
                  {[0, 1, 2].map((item) => (
                    <div className="skeleton" key={item} />
                  ))}
                </div>
              ) : view.page === "instance" && view.tab === "env" && currentInspection?.error ? (
                <Empty title={currentInspection.error} />
              ) : view.page === "instance" && view.tab === "env" && !currentInspection ? (
                <Empty title="Loading environment…" />
              ) : rows.length === 0 ? (
                <Empty title={filter ? "No matches" : empty[0]} hint={filter ? "Esc clears the search." : empty[1]} />
              ) : (
                <ul className="list" role="listbox" aria-label={listTitle}>
                  {rows.map((row) => (
                    <li
                      key={row.id}
                      id={`row-${row.id}`}
                      role="option"
                      aria-selected={row.id === selectedID}
                      className={`row${row.id === selectedID ? " selected" : ""}${row.activate ? " clickable" : ""}`}
                      onClick={() => select(row.id)}
                      onDoubleClick={() => (row.activate ? row.activate() : setExpanded(row.expand))}
                    >
                      {row.primary}
                      {row.activate && view.page !== "instance" && (
                        <button
                          className="row-open"
                          onClick={(event) => {
                            event.stopPropagation();
                            row.activate?.();
                          }}
                          aria-label="Open"
                          title="Open (Enter)"
                        >
                          <Icon name="chevron" />
                        </button>
                      )}
                      {row.activate && view.page === "instance" && view.tab === "services" && (
                        <button
                          className="row-open"
                          onClick={(event) => {
                            event.stopPropagation();
                            row.activate?.();
                          }}
                          aria-label="Open service URLs"
                          title="Open service (Enter)"
                        >
                          <Icon name="chevron" />
                        </button>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </section>
            <aside className="detail-pane" aria-label="Selection details">
              {selected ? selected.detail : <p className="muted">Select a row to see details.</p>}
            </aside>
          </div>
        )}
      </main>

      <footer className="footer">
        <span>
          <Kbd2 k="?" /> Shortcuts
        </span>
        <span>
          <Kbd2 k="/" /> Search
        </span>
        <span>
          <Kbd2 k="Enter" /> Open
        </span>
        <span>
          <Kbd2 k="c" /> Copy
        </span>
        <span>
          <Kbd2 k="p" /> {paused ? "Resume" : "Pause"}
        </span>
        {snapshot?.webui.subdomain && <span className="footer-end muted">Instances keep running when you close this page.</span>}
      </footer>

      {confirm && (
        <Modal
          title={`${confirm.operation === "stop" ? "Stop" : "Restart"} ${confirm.instance.name}?`}
          onClose={() => setConfirm(null)}
          footer={
            <>
              <Button onClick={() => setConfirm(null)}>Cancel</Button>
              <Button autoFocus variant={confirm.operation === "stop" ? "danger" : "primary"} icon={confirm.operation === "stop" ? "stop" : "restart"} onClick={() => void runControl()}>
                {confirm.operation === "stop" ? "Stop instance" : "Restart instance"}
              </Button>
            </>
          }
        >
          <p>
            This {confirm.operation === "stop" ? "gracefully stops" : "restarts"} every service in <strong>{confirm.instance.name}</strong> ({confirm.instance.profile}).
          </p>
          <p className="muted small mono">{confirm.instance.worktree}</p>
        </Modal>
      )}
      {help && <HelpModal onClose={() => setHelp(false)} />}
      {expanded && (
        <Modal
          title={expanded.title}
          onClose={() => setExpanded(null)}
          wide
          footer={
            <>
              {expanded.url && (
                <a className="btn btn-default" href={expanded.url} target="_blank" rel="noopener noreferrer">
                  <Icon name="external" /> <span>Open</span>
                </a>
              )}
              <Button
                icon="copy"
                onClick={() =>
                  void (expanded.copy ? expanded.copy() : copyText(expanded.text).then(() => toast("Copied"), (error) => handleError(error, "Copy failed: ")))
                }
              >
                Copy text
              </Button>
            </>
          }
        >
          <pre className="expanded">{expanded.text}</pre>
        </Modal>
      )}
      <Toasts toasts={toasts} dismiss={(id) => setToasts((items) => items.filter((item) => item.id !== id))} />
      {busy && <div className="busy-bar" aria-hidden="true" />}
    </div>
  );
}

function Kbd2({ k }: { k: string }) {
  return <kbd className="kbd">{k}</kbd>;
}

export type { View };
