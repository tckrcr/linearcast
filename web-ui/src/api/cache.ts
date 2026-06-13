import type {
  CacheSummary,
  EncodeReclaimResponse,
  InvalidProfilePackageCleanupResponse,
  MissingMediaMaintenanceResponse,
  OptimizeDBMaintenanceResponse,
  OrphanPackagesMaintenanceResponse,
  PackageIntegrityResponse,
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

// getPackageIntegrity inspects ready packages read-only. With a media id it
// reports that media's full ladder; without one it sweeps every ready package.
export async function getPackageIntegrity(media?: string) {
  return apiFetch<PackageIntegrityResponse>("/api/admin/maintenance/package-integrity", {
    cache: "no-store",
    query: { media: media?.trim() || undefined },
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
