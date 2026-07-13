import type {
  CacheSummary,
  EncodeReclaimResponse,
  ImportPackagesResponse,
  InvalidProfilePackageCleanupResponse,
  MissingMediaMaintenanceResponse,
  OptimizeDBMaintenanceResponse,
  OrphanPackagesMaintenanceResponse,
  PackageIntegrityRepairResponse,
  PackageIntegrityResponse,
  PackageRequeueResponse,
  ScheduleCheckResponse,
} from "../types";
import { apiFetch } from "./client";

export async function cleanupInvalidProfilePackages(dryRun: boolean) {
  return apiFetch<InvalidProfilePackageCleanupResponse>("/api/cache/invalid-profiles", {
    method: "DELETE",
    query: { "dry-run": dryRun },
  });
}

export async function getCacheSummary() {
  return apiFetch<CacheSummary>("/api/cache/summary", { cache: "no-store" });
}

export async function cleanupMissingMedia(dryRun: boolean) {
  return apiFetch<MissingMediaMaintenanceResponse>("/api/admin/maintenance/missing-media", {
    method: "DELETE",
    query: { "dry-run": dryRun },
  });
}

export async function cleanupOrphanPackages(dryRun: boolean) {
  return apiFetch<OrphanPackagesMaintenanceResponse>("/api/admin/maintenance/orphan-packages", {
    method: "DELETE",
    query: { "dry-run": dryRun },
  });
}

export async function optimizeDatabase() {
  return apiFetch<OptimizeDBMaintenanceResponse>("/api/admin/maintenance/optimize-db", {
    method: "POST",
  });
}

// importPackages rebuilds DB rows for finalized packages already on disk whose
// media rows exist (e.g. after rescanning source folders into a fresh DB),
// without re-encoding.
export async function importPackages() {
  return apiFetch<ImportPackagesResponse>("/api/admin/maintenance/import-packages", {
    method: "POST",
  });
}

// getPackageIntegrity inspects ready packages read-only. With a media id it
// reports that media's full ladder; without one it sweeps every ready package.
export async function getPackageIntegrity(media?: string) {
  return apiFetch<PackageIntegrityResponse>("/api/admin/maintenance/package-integrity", {
    cache: "no-store",
    query: { media: media?.trim() || undefined },
  });
}

// repairPackageIntegrity triggers the same sweep the encoder sweeper runs —
// ready packages with missing/bad files or truncated durations are requeued.
export async function repairPackageIntegrity() {
  return apiFetch<PackageIntegrityRepairResponse>("/api/admin/maintenance/package-integrity", {
    method: "POST",
  });
}

// requeuePackage requeues a single ready package for re-encoding by ID.
export async function requeuePackage(packageId: string) {
  return apiFetch<PackageRequeueResponse>(
    `/api/admin/maintenance/packages/${encodeURIComponent(packageId)}/requeue`,
    { method: "POST" },
  );
}

// checkSchedule runs the full schedule integrity audit and returns structured issues.
export async function checkSchedule(opts?: {
  channel?: string;
  hours?: number;
  from?: string;
  gapMs?: number;
  all?: boolean;
}) {
  return apiFetch<ScheduleCheckResponse>("/api/admin/maintenance/schedule-check", {
    cache: "no-store",
    query: {
      channel: opts?.channel || undefined,
      hours: opts?.hours,
      from: opts?.from || undefined,
      "gap-ms": opts?.gapMs,
      all: opts?.all || undefined,
    },
  });
}

// reclaimMediaEncodes deletes a media's package rows and on-disk artifacts.
// Referenced media are skipped unless force is set; dryRun previews without
// touching anything.
export async function reclaimMediaEncodes(
  media: string,
  opts: { profile?: string; force?: boolean; dryRun: boolean },
) {
  return apiFetch<EncodeReclaimResponse>("/api/admin/maintenance/packages", {
    method: "DELETE",
    query: {
      media,
      profile: opts.profile?.trim() || undefined,
      force: opts.force || undefined,
      "dry-run": opts.dryRun,
    },
  });
}
