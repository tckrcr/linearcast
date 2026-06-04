import type {
  CacheSummary,
  InvalidProfilePackageCleanupResponse,
  MissingMediaMaintenanceResponse,
  OptimizeDBMaintenanceResponse,
  OrphanPackagesMaintenanceResponse,
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
