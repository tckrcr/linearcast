import { apiFetch } from "./client";
import type { PlayableSourcesResponseDTO } from "./dto";

export async function getPlayableSources(signal?: AbortSignal) {
  return apiFetch<PlayableSourcesResponseDTO>("/api/playable-sources", {
    cache: "no-store",
    signal,
  });
}
