import { apiFetch } from "./client";
import type {
  EncoderDownloadsResponse,
  EncoderListResponse,
  EncoderRegisterResponse,
} from "../types";

export function getEncoders(signal?: AbortSignal): Promise<EncoderListResponse> {
  return apiFetch<EncoderListResponse>("/api/admin/encoders", { cache: "no-store", signal });
}

export function registerEncoder(name: string): Promise<EncoderRegisterResponse> {
  return apiFetch<EncoderRegisterResponse>("/api/admin/encoders", {
    method: "POST",
    json: { name },
  });
}

export function revokeEncoder(id: string): Promise<{ ok: boolean }> {
  return apiFetch<{ ok: boolean }>(`/api/admin/encoders/${encodeURIComponent(id)}/revoke`, {
    method: "POST",
  });
}

export function deleteEncoder(id: string): Promise<{ ok: boolean }> {
  return apiFetch<{ ok: boolean }>(`/api/admin/encoders/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
}

export function updateEncoderConcurrency(id: string, concurrency: number): Promise<{ ok: boolean }> {
  return apiFetch<{ ok: boolean }>(`/api/admin/encoders/${encodeURIComponent(id)}`, {
    method: "PATCH",
    json: { concurrency },
  });
}

export function updateLocalWorker(payload: { concurrency?: number }): Promise<{ ok: boolean }> {
  return apiFetch<{ ok: boolean }>("/api/admin/local-worker", {
    method: "PUT",
    json: payload,
  });
}

export function getEncoderDownloads(): Promise<EncoderDownloadsResponse> {
  return apiFetch<EncoderDownloadsResponse>("/api/admin/encoders/downloads", { cache: "no-store" });
}

// encoderDownloadURL returns the path the browser navigates to in order to
// fetch a specific platform binary. Same-origin so the admin session cookie
// is sent automatically.
export function encoderDownloadURL(platform: string): string {
  return `/api/admin/encoders/download/${encodeURIComponent(platform)}`;
}
