export interface ServiceState {
  state: string;
  pid?: number;
  ready: boolean;
  started_at?: string;
  ended_at?: string;
  last_error?: string;
}

export interface RouteState {
  state: string;
  project: string;
  branch: string;
  host?: string;
  url?: string;
  optional: boolean;
}

export interface Instance {
  session_id: string;
  name: string;
  url?: string;
  worktree: string;
  profile: string;
  started_at: string;
  ports?: Record<string, number>;
  services: Record<string, ServiceState>;
  routes?: Record<string, RouteState>;
  endpoints?: Record<string, string>;
  log_paths?: Record<string, string>;
  last_error?: string;
}

export interface Route {
  project: string;
  branch: string;
  port: number;
  host?: string;
  domain: string;
  created_at: string;
  expires_at: string;
  source?: string;
  owner?: string;
}

export interface WebUIStatus {
  url?: string;
  subdomain: string;
  listen?: string;
  route_error?: string;
}

export interface Snapshot {
  instances: Instance[];
  routes: Route[];
  daemon?: Record<string, unknown>;
  error?: string;
  webui: WebUIStatus;
  generated_at: string;
}

export interface Inspection {
  project: string;
  branch: string;
  worktree: string;
  profile: string;
  environment: Record<string, Record<string, string>>;
}

export interface LogsResponse {
  lines: string[];
  error?: string;
}

export type Operation = "stop" | "restart";
