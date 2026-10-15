import type { ReactNode } from "react";
import {
  backendHost,
  daemonLabels,
  formatDaemonValue,
  formatTime,
  formatValue,
  instanceTone,
  instanceURL,
  readyCount,
  relativeTime,
  routeID,
  serviceNames,
  serviceTone,
  urlRows,
} from "./logic";
import type { Inspection, Instance, Operation, Snapshot } from "./types";
import { Badge, Button, Field, StatusDot, URLText } from "./components/ui";

export interface Expanded {
  title: string;
  text: string;
  url?: string;
  /** Overrides copying the displayed text, e.g. for hidden values. */
  copy?: () => Promise<void>;
}

export interface Row {
  id: string;
  search: string;
  url?: string;
  primary: ReactNode;
  detail: ReactNode;
  expand: Expanded;
  activate?: () => void;
  /** Copies the row's value; entry copies NAME=value for environment rows. */
  copy?: (entry: boolean) => Promise<void>;
}

export interface RowActions {
  openInstance: (id: string) => void;
  openService: (name: string, tab?: "urls" | "logs") => void;
  control: (operation: Operation, instance: Instance) => void;
  copyURL: (url: string) => Promise<void>;
  copyEnv: (name: string, entry: boolean) => Promise<void>;
  expand: (value: Expanded) => void;
}

const sortKeys = <T,>(record: Record<string, T> | undefined) => Object.keys(record ?? {}).sort();

function URLField({ url, actions }: { url: string; actions: RowActions }) {
  return (
    <Field label="URL">
      <div className="url-field">
        <URLText url={url} />
        <span className="inline-actions">
          <Button small icon="copy" title="Copy URL (c)" onClick={() => void actions.copyURL(url)} />
          <a className="btn btn-default btn-small btn-icon" href={url} target="_blank" rel="noopener noreferrer" title="Open in a new tab (o)" aria-label="Open in a new tab">
            <svg className="icon" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M14 4h6v6" />
              <path d="M20 4 10 14" />
              <path d="M19 14v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h4" />
            </svg>
          </a>
        </span>
      </div>
    </Field>
  );
}

function Readiness({ instance }: { instance: Instance }) {
  const { ready, total } = readyCount(instance);
  const tone = instanceTone(instance);
  return (
    <Badge tone={tone}>
      <StatusDot tone={tone} />
      {ready}/{total} ready
    </Badge>
  );
}

export function instanceRows(snapshot: Snapshot, actions: RowActions, now: number): Row[] {
  return snapshot.instances.map((instance) => {
    const url = instanceURL(instance);
    const services = serviceNames(instance);
    return {
      id: instance.session_id,
      search: [instance.name, instance.profile, instance.worktree, ...services].join(" "),
      url,
      activate: () => actions.openInstance(instance.session_id),
      copy: url ? () => actions.copyURL(url) : undefined,
      expand: {
        title: instance.name,
        text: [instance.name, instance.profile, instance.worktree, url, instance.last_error].filter(Boolean).join("\n"),
        url,
      },
      primary: (
        <div className="row-main">
          <div className="row-title">
            <span className="truncate">{instance.name}</span>
            <span className="chip">{instance.profile}</span>
          </div>
          <div className="row-sub truncate mono">{instance.worktree}</div>
          <div className="row-end">
            <Readiness instance={instance} />
          </div>
        </div>
      ),
      detail: (
        <>
          <div className="detail-hero">
            <h3>{instance.name}</h3>
            <Readiness instance={instance} />
          </div>
          <div className="detail-actions">
            <Button variant="primary" icon="chevron" onClick={() => actions.openInstance(instance.session_id)}>
              Open instance
            </Button>
            <Button icon="restart" onClick={() => actions.control("restart", instance)}>
              Restart
            </Button>
            <Button icon="stop" variant="danger" onClick={() => actions.control("stop", instance)}>
              Stop
            </Button>
          </div>
          <Field label="Profile">{instance.profile}</Field>
          <Field label="Worktree" mono>
            {instance.worktree}
          </Field>
          {!instance.started_at.startsWith("0001-") && (
            <Field label="Started">
              {formatTime(instance.started_at)} <span className="muted">· {relativeTime(instance.started_at, now)}</span>
            </Field>
          )}
          {url && <URLField url={url} actions={actions} />}
          <Field label="Services">
            <div className="chips">
              {services.map((name) => {
                const service = instance.services[name]!;
                return (
                  <span className="chip" key={name}>
                    <StatusDot tone={serviceTone(service)} /> {name}
                  </span>
                );
              })}
              {services.length === 0 && <span className="muted">None</span>}
            </div>
          </Field>
          {instance.last_error && (
            <Field label="Error">
              <span className="error-text">{instance.last_error}</span>
            </Field>
          )}
        </>
      ),
    };
  });
}

export function routeRows(snapshot: Snapshot, actions: RowActions, now: number): Row[] {
  return [...snapshot.routes]
    .sort((a, b) => (a.domain < b.domain ? -1 : a.domain > b.domain ? 1 : 0))
    .map((route) => {
      const id = routeID(route);
      const url = "https://" + route.domain;
      const backend = `${backendHost(route.host)}:${route.port}`;
      const expires = route.expires_at && !route.expires_at.startsWith("0001-") ? route.expires_at : "";
      return {
        id,
        search: [id, url, backend, route.source].join(" "),
        url,
        copy: () => actions.copyURL(url),
        expand: { title: id, text: `${id}\n${url}\n→ ${backend}`, url },
        primary: (
          <div className="row-main">
            <div className="row-title">
              <span className="truncate">{id}</span>
              {route.source && <span className="chip">{route.source}</span>}
            </div>
            <div className="row-sub truncate">
              <URLText url={url} /> <span className="muted">→ {backend}</span>
            </div>
          </div>
        ),
        detail: (
          <>
            <div className="detail-hero">
              <h3>{id}</h3>
            </div>
            <URLField url={url} actions={actions} />
            <Field label="Backend" mono>
              {backend}
            </Field>
            <Field label="Source">{route.source || "manual"}</Field>
            {expires && (
              <Field label="Expires">
                {formatTime(expires)} <span className="muted">· {relativeTime(expires, now)}</span>
              </Field>
            )}
            {route.created_at && !route.created_at.startsWith("0001-") && <Field label="Created">{formatTime(route.created_at)}</Field>}
          </>
        ),
      };
    });
}

export function daemonRows(snapshot: Snapshot): Row[] {
  const daemon = snapshot.daemon ?? {};
  return sortKeys(daemon).map((key) => {
    const value = formatDaemonValue(key, daemon[key]);
    const raw = formatValue(daemon[key]);
    const label = daemonLabels[key] ?? key;
    const tone =
      key === "ready" ? (daemon[key] ? "good" : "bad") : key === "last_error" && raw ? "bad" : key === "traefik_https_listener" ? (raw.startsWith("listening") ? "good" : "bad") : undefined;
    return {
      id: key,
      search: `${key} ${label} ${raw}`,
      expand: { title: label, text: raw },
      copy: undefined,
      primary: (
        <div className="row-kv">
          <span className="row-key">{label}</span>
          <span className="row-value truncate">
            {tone && <StatusDot tone={tone} />} {value || <span className="muted">—</span>}
          </span>
        </div>
      ),
      detail: (
        <>
          <div className="detail-hero">
            <h3>{label}</h3>
          </div>
          <Field label="Key" mono>
            {key}
          </Field>
          <Field label="Value" mono>
            <span className="wrap">{raw || "—"}</span>
          </Field>
        </>
      ),
    };
  });
}

export function serviceRows(instance: Instance, actions: RowActions): Row[] {
  return serviceNames(instance).map((name) => {
    const service = instance.services[name]!;
    const url = instanceURL(instance, name);
    const tone = serviceTone(service);
    const logPath = instance.log_paths?.[name];
    return {
      id: name,
      search: `${name} ${service.description ?? ""} ${service.state} ${service.pid ?? ""} ${service.last_error ?? ""}`,
      url,
      activate: () => actions.openService(name),
      copy: url ? () => actions.copyURL(url) : undefined,
      expand: { title: name, text: [name, service.description, service.state, url, service.last_error].filter(Boolean).join("\n"), url },
      primary: (
        <div className="row-main">
          <div className="row-title">
            <span className="truncate">{name}</span>
          </div>
          <div className="row-sub">
            {service.description && <div className="truncate">{service.description}</div>}
            <div className="truncate">{service.pid ? `PID ${service.pid}` : "No process"}{service.last_error ? ` · ${service.last_error}` : ""}</div>
          </div>
          <div className="row-end">
            <Badge tone={tone}>
              <StatusDot tone={tone} pulse={tone === "warn"} /> {service.state}
            </Badge>
          </div>
        </div>
      ),
      detail: (
        <>
          <div className="detail-hero">
            <h3>{name}</h3>
            <Badge tone={tone}>
              <StatusDot tone={tone} /> {service.state}
            </Badge>
          </div>
          {service.description && (
            <Field label="Description">
              <span className="wrap">{service.description}</span>
            </Field>
          )}
          <div className="detail-actions">
            <Button icon="layers" onClick={() => actions.openService(name, "urls")}>
              URLs
            </Button>
            <Button icon="logs" onClick={() => actions.openService(name, "logs")}>
              Logs
            </Button>
          </div>
          <Field label="Process">{service.pid ? `PID ${service.pid}` : "—"}</Field>
          {url && <URLField url={url} actions={actions} />}
          {logPath && (
            <Field label="Log file" mono>
              <span className="wrap">{logPath}</span>
            </Field>
          )}
          {sortKeys(instance.ports).length > 0 && (
            <Field label="Instance ports">
              <div className="chips">
                {sortKeys(instance.ports).map((port) => (
                  <span className="chip mono" key={port}>
                    {port} : {instance.ports![port]}
                  </span>
                ))}
              </div>
            </Field>
          )}
          {service.last_error && (
            <Field label="Error">
              <span className="error-text">{service.last_error}</span>
            </Field>
          )}
        </>
      ),
    };
  });
}

export function urlListRows(instance: Instance, service: string, actions: RowActions): Row[] {
  return urlRows(instance, service).map((row) => ({
    id: row.id,
    search: `${row.id} ${row.url} ${row.state ?? ""}`,
    url: row.url,
    copy: () => actions.copyURL(row.url),
    activate: () => actions.expand({ title: row.id, text: row.url, url: row.url }),
    expand: { title: row.id, text: row.url, url: row.url },
    primary: (
      <div className="row-main">
        <div className="row-title">
          <span className="truncate">{row.kind === "public" ? row.name : row.id}</span>
          <span className={`chip${row.kind === "public" ? " chip-accent" : ""}`}>{row.kind === "public" ? "public" : "local"}</span>
          {row.state && <span className="chip">{row.state}</span>}
        </div>
        <div className="row-sub truncate">
          <URLText url={row.url} />
        </div>
      </div>
    ),
    detail: (
      <>
        <div className="detail-hero">
          <h3>{row.id}</h3>
        </div>
        <Field label="Endpoint" mono>
          {row.id}
        </Field>
        <URLField url={row.url} actions={actions} />
        {row.state && <Field label="Route state">{row.state}</Field>}
      </>
    ),
  }));
}

export function envRows(inspection: Inspection | undefined, service: string, actions: RowActions): Row[] {
  const values = inspection?.environment[service] ?? {};
  return sortKeys(values).map((name) => {
    const value = values[name] ?? "";
    const redacted = value === "<redacted>";
    return {
      id: name,
      search: `${name} ${redacted ? "" : value}`,
      copy: (entry) => actions.copyEnv(name, entry),
      activate: () => actions.expand({ title: name, text: value, copy: () => actions.copyEnv(name, false) }),
      expand: { title: name, text: value, copy: () => actions.copyEnv(name, false) },
      primary: (
        <div className="row-kv">
          <span className="row-key mono truncate">{name}</span>
          <span className={`row-value mono truncate${redacted ? " redacted" : ""}`}>{redacted ? "••••••••" : value}</span>
        </div>
      ),
      detail: (
        <>
          <div className="detail-hero">
            <h3 className="mono">{name}</h3>
          </div>
          <div className="detail-actions">
            <Button icon="copy" onClick={() => void actions.copyEnv(name, false)}>
              Copy value
            </Button>
            <Button icon="copy" onClick={() => void actions.copyEnv(name, true)}>
              Copy NAME=value
            </Button>
          </div>
          <Field label="Value" mono>
            <span className={`wrap${redacted ? " redacted" : ""}`}>{redacted ? "Hidden — sensitive" : value || "(empty)"}</span>
          </Field>
          <p className="muted small">Copy works while values stay hidden.</p>
        </>
      ),
    };
  });
}
