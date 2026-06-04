import type { PlayableSourcesResponse } from "../types";
import { apiFetch } from "./client";

export async function getPlayableSources(signal?: AbortSignal) {
  return apiFetch<PlayableSourcesResponse>("/api/playable-sources", {
    cache: "no-store",
    signal,
  });
}
