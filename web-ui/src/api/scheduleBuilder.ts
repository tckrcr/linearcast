import { apiFetch } from "./client";
import type {
  CreateChannelRequestDTO,
  CreateChannelResponseDTO,
  FillerAssetCandidateListDTO,
  MediaSourceStatusDTO,
  ScheduleBuilderEntryInputDTO,
} from "./dto";

export function getScheduleBuilderSourceStatus(): Promise<MediaSourceStatusDTO> {
  return apiFetch<MediaSourceStatusDTO>("/api/admin/media-sources/status", { cache: "no-store" });
}

export function getScheduleBuilderFillerCandidates(
  profile?: string,
  signal?: AbortSignal,
): Promise<FillerAssetCandidateListDTO> {
  return apiFetch<FillerAssetCandidateListDTO>("/api/filler-assets/candidates", {
    cache: "no-store",
    signal,
    query: { profile },
  });
}

// One client-composed schedule row. start is implied by contiguous laying, so
// only the media, its play offset, and its on-air duration are sent.
export type ScheduleBuilderEntryInput = ScheduleBuilderEntryInputDTO;

export function createScheduleBuilderChannel(req: CreateChannelRequestDTO): Promise<CreateChannelResponseDTO> {
  return apiFetch<CreateChannelResponseDTO>("/api/channels", {
    method: "POST",
    json: req,
  });
}
