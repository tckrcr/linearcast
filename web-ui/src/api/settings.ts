import type { EncoderSweeperSettings, PublicServerURL, SchedulerTunables, SubtitleSettings } from "../types";
import { apiFetch } from "./client";

export async function getSchedulerTunables() {
  return apiFetch<SchedulerTunables>("/api/admin/scheduler-tunables", {
    cache: "no-store",
  });
}

export async function updateSchedulerTunables(tunables: SchedulerTunables) {
  return apiFetch<SchedulerTunables>("/api/admin/scheduler-tunables", {
    method: "PUT",
    json: tunables,
  });
}

export async function getEncoderSweeperSettings() {
  return apiFetch<EncoderSweeperSettings>("/api/admin/encoder-sweeper-settings", {
    cache: "no-store",
  });
}

export async function updateEncoderSweeperSettings(settings: EncoderSweeperSettings) {
  return apiFetch<EncoderSweeperSettings>("/api/admin/encoder-sweeper-settings", {
    method: "PUT",
    json: settings,
  });
}

export async function getSubtitleSettings() {
  return apiFetch<SubtitleSettings>("/api/subtitle-settings", {
    cache: "no-store",
  });
}

export async function updateSubtitleSettings(settings: SubtitleSettings) {
  return apiFetch<SubtitleSettings>("/api/subtitle-settings", {
    method: "PUT",
    json: settings,
  });
}

export async function getPublicServerURL() {
  return apiFetch<PublicServerURL>("/api/public-server-url", {
    cache: "no-store",
  });
}

export async function updatePublicServerURL(publicServerUrl: string) {
  return apiFetch<PublicServerURL>("/api/public-server-url", {
    method: "PUT",
    json: { publicServerUrl },
  });
}
