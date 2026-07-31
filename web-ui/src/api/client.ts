import type {
  AdminAuthStatusDTO,
  AdminLoginRequestDTO,
  AdminLogoutResponseDTO,
  AdminPasswordChangeRequestDTO,
  ErrorResponseDTO,
} from "./dto";

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

async function readBody(response: Response): Promise<unknown> {
  return response.json().catch(() => null);
}

export class ApiError extends Error {
  code?: string;
  body?: unknown;
  status: number;
  constructor(message: string, status: number, body?: ErrorResponseDTO | unknown) {
    super(message);
    this.status = status;
    this.code = isErrorResponse(body) ? body.error : undefined;
    this.body = body;
  }
}

function isErrorResponse(body: unknown): body is ErrorResponseDTO {
  return typeof body === "object" && body !== null
    && typeof (body as { error?: unknown }).error === "string"
    && typeof (body as { message?: unknown }).message === "string";
}

function failure(response: Response, body: unknown): ApiError {
  const message = isErrorResponse(body) ? body.message : `admin api ${response.status}`;
  return new ApiError(isErrorResponse(body) && body.hint ? `${message} ${body.hint}` : message, response.status, body);
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
  const response = await apiFetchRaw(path, options);
  const body = await readBody(response);
  if (!response.ok) {
    throw failure(response, body);
  }
  return body as T;
}

// apiFetchRaw applies the shared request construction and session-expiry
// notification, but deliberately returns non-2xx responses unchanged. Manifest
// probes use the status code to distinguish "warming" from network failure.
export async function apiFetchRaw(
  path: string,
  options: ApiFetchOptions = {},
): Promise<Response> {
  const { json, query, headers, ...rest } = options;
  const init: RequestInit = { ...rest };
  const requestHeaders = new Headers(headers);
  if (json !== undefined) {
    init.body = JSON.stringify(json);
    if (!requestHeaders.has("Content-Type")) {
      requestHeaders.set("Content-Type", "application/json");
    }
  }
  if ([...requestHeaders].length > 0) {
    init.headers = requestHeaders;
  }
  const response = await fetch(buildPath(path, query), init);
  if (response.status === 401) notifyUnauthorized(path);
  return response;
}

export function getAdminAuthStatus(): Promise<AdminAuthStatusDTO> {
  return apiFetch<AdminAuthStatusDTO>("/api/auth/status", { cache: "no-store" });
}

export function loginAdmin(password: string): Promise<AdminAuthStatusDTO> {
  const request: AdminLoginRequestDTO = { password };
  return apiFetch<AdminAuthStatusDTO>("/api/auth/login", {
    method: "POST",
    json: request,
  });
}

export function logoutAdmin(): Promise<AdminLogoutResponseDTO> {
  return apiFetch<AdminLogoutResponseDTO>("/api/auth/logout", { method: "POST" });
}

export function changeAdminPassword(currentPassword: string, newPassword: string): Promise<AdminAuthStatusDTO> {
  const request: AdminPasswordChangeRequestDTO = { currentPassword, newPassword };
  return apiFetch<AdminAuthStatusDTO>("/api/auth/change-password", {
    method: "POST",
    json: request,
  });
}

export function channelPath(channelID: string, suffix = ""): string {
  return `/api/channels/${encodeURIComponent(channelID)}${suffix}`;
}
