export type QueryValue = string | number | boolean | undefined | null;

export function buildPath(path: string, params?: Record<string, QueryValue>): string {
  if (!params) return path;
  const url = new URL(path, window.location.origin);
  for (const [key, value] of Object.entries(params)) {
    if (value == null) continue;
    url.searchParams.set(key, String(value));
  }
  return url.pathname + url.search;
}

export type ApiFetchOptions = Omit<RequestInit, "body"> & {
  json?: unknown;
  query?: Record<string, QueryValue>;
};

export type AdminAuthStatus = {
  enabled: boolean;
  authenticated: boolean;
  mustChange?: boolean;
};

async function readBody(response: Response): Promise<any> {
  return response.json().catch(() => null);
}

export class ApiError extends Error {
  code?: string;
  body?: unknown;
  status: number;
  constructor(message: string, status: number, body?: unknown) {
    super(message);
    this.status = status;
    this.code = (body as any)?.code;
    this.body = body;
  }
}

function failure(response: Response, body: any): ApiError {
  const message = body?.message || `admin api ${response.status}`;
  return new ApiError(body?.hint ? `${message} ${body.hint}` : message, response.status, body);
}

// UNAUTHORIZED_EVENT fires whenever a request 401s on a non-auth endpoint —
// i.e. the session expired out from under an already-loaded page (e.g. after a
// redeploy restarts the admin). The admin shell listens for it and drops back
// to the login screen instead of leaving the operator on a half-broken page.
export const UNAUTHORIZED_EVENT = "linearcast:unauthorized";

function notifyUnauthorized(path: string) {
  if (typeof window === "undefined") return;
  // Login / logout / status / change-password report 401 as normal flow
  // (wrong password, no session yet) and drive their own UI.
  if (path.startsWith("/api/auth/")) return;
  window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
}

export async function apiFetch<T = unknown>(
  path: string,
  options: ApiFetchOptions = {},
): Promise<T> {
  const { json, query, headers, ...rest } = options;
  const init: RequestInit = { ...rest };
  if (json !== undefined) {
    init.body = JSON.stringify(json);
    init.headers = { "Content-Type": "application/json", ...(headers || {}) };
  } else if (headers) {
    init.headers = headers;
  }
  const response = await fetch(buildPath(path, query), init);
  const body = await readBody(response);
  if (!response.ok) {
    if (response.status === 401) notifyUnauthorized(path);
    throw failure(response, body);
  }
  return body as T;
}

export async function apiFetchRaw(
  path: string,
  options: ApiFetchOptions = {},
): Promise<Response> {
  const { json, query, headers, ...rest } = options;
  const init: RequestInit = { ...rest };
  if (json !== undefined) {
    init.body = JSON.stringify(json);
    init.headers = { "Content-Type": "application/json", ...(headers || {}) };
  } else if (headers) {
    init.headers = headers;
  }
  const response = await fetch(buildPath(path, query), init);
  if (!response.ok) {
    if (response.status === 401) notifyUnauthorized(path);
    const body = await readBody(response);
    throw failure(response, body);
  }
  return response;
}

export function getAdminAuthStatus(): Promise<AdminAuthStatus> {
  return apiFetch<AdminAuthStatus>("/api/auth/status", { cache: "no-store" });
}

export function loginAdmin(password: string): Promise<AdminAuthStatus> {
  return apiFetch<AdminAuthStatus>("/api/auth/login", {
    method: "POST",
    json: { password },
  });
}

export function logoutAdmin(): Promise<{ authenticated: boolean }> {
  return apiFetch<{ authenticated: boolean }>("/api/auth/logout", { method: "POST" });
}

export function changeAdminPassword(currentPassword: string, newPassword: string): Promise<AdminAuthStatus> {
  return apiFetch<AdminAuthStatus>("/api/auth/change-password", {
    method: "POST",
    json: { currentPassword, newPassword },
  });
}

export function channelPath(channelID: string, suffix = ""): string {
  return `/api/channels/${encodeURIComponent(channelID)}${suffix}`;
}
