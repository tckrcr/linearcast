import type { AdminWriteLogEntry } from "../types";
import { apiFetch } from "./client";

export async function getAdminWriteLog(limit = 100, signal?: AbortSignal) {
  return apiFetch<{ entries: AdminWriteLogEntry[] }>("/api/admin/write-log", {
    query: { limit },
    cache: "no-store",
    signal,
  });
}
