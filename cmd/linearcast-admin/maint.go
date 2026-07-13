package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packager"
)

func runMaint(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		maintUsage()
		return
	}
	switch args[0] {
	case "delete-encode":
		maintDeleteEncode(args[1:])
	case "audit-duration":
		maintAuditDuration(args[1:])
	case "backfill-package-bytes":
		maintBackfillPackageBytes(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown maint subcommand: %s\n\n", args[0])
		maintUsage()
		os.Exit(2)
	}
}

func maintUsage() {
	fmt.Fprintln(os.Stderr, "Usage: linearcast-admin maint <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "  Env: LINEARCAST_DB")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  delete-encode <mediaID> [--profile <profile>] [--force]")
	fmt.Fprintln(os.Stderr, "    Delete encoded package data for a media item.")
	fmt.Fprintln(os.Stderr, "    Removes media_packages + packaged_segments rows and the on-disk package_root.")
	fmt.Fprintln(os.Stderr, "    Warns and aborts if the media has future schedule entries; --force overrides.")
	fmt.Fprintln(os.Stderr, "    Requires --force to skip the confirmation prompt.")
	fmt.Fprintln(os.Stderr, "  audit-duration [--fix]")
	fmt.Fprintln(os.Stderr, "    List ready packages whose packaged duration is short of the source (truncated encode),")
	fmt.Fprintln(os.Stderr, "    using the same tolerance as the finalize guard. Read-only by default.")
	fmt.Fprintln(os.Stderr, "    --fix marks each offender pending in place so the worker re-encodes it immediately.")
	fmt.Fprintln(os.Stderr, "  backfill-package-bytes [--dry-run]")
	fmt.Fprintln(os.Stderr, "    Fill media_packages.package_bytes for ready packages from DB-tracked init/segment paths.")
}

func maintBackfillPackageBytes(args []string) {
	fs := flag.NewFlagSet("backfill-package-bytes", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "print package byte totals without updating media_packages")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: linearcast-admin maint backfill-package-bytes [--dry-run]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() != 0 {
		fs.Usage()
		os.Exit(2)
	}

	dbPath := os.Getenv("LINEARCAST_DB")
	if dbPath == "" {
		log.Fatal("LINEARCAST_DB is required")
	}

	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	rows, err := conn.QueryContext(ctx, `
		SELECT id, COALESCE(init_segment_path, '')
		FROM media_packages
		WHERE status = ? AND package_bytes IS NULL
		ORDER BY updated_at_ms DESC, id`, string(db.PackageStatusReady))
	if err != nil {
		log.Fatalf("query packages: %v", err)
	}
	defer rows.Close()

	type target struct {
		id       string
		initPath string
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.initPath); err != nil {
			log.Fatalf("scan package: %v", err)
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("scan packages: %v", err)
	}

	var updated, skipped int
	var totalBytes int64
	nowMs := time.Now().UTC().UnixMilli()
	for _, t := range targets {
		bytes, err := packageBytesFromTrackedFiles(ctx, conn, t.id, t.initPath)
		if err != nil {
			skipped++
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", t.id, err)
			continue
		}
		if !*dryRun {
			if _, err := conn.ExecContext(ctx,
				`UPDATE media_packages SET package_bytes = ?, updated_at_ms = ? WHERE id = ? AND package_bytes IS NULL`,
				bytes, nowMs, t.id); err != nil {
				log.Fatalf("update %s: %v", t.id, err)
			}
		}
		updated++
		totalBytes += bytes
		fmt.Printf("%s\t%d\n", t.id, bytes)
	}
	if *dryRun {
		fmt.Printf("\nwould backfill %d package(s), skipped %d, total %d bytes\n", updated, skipped, totalBytes)
		return
	}
	fmt.Printf("\nbackfilled %d package(s), skipped %d, total %d bytes\n", updated, skipped, totalBytes)
}

func packageBytesFromTrackedFiles(ctx context.Context, conn *sql.DB, packageID, initPath string) (int64, error) {
	var total int64
	if initPath != "" {
		n, err := fileSize(initPath)
		if err != nil {
			return 0, fmt.Errorf("stat init %q: %w", initPath, err)
		}
		total += n
	}

	rows, err := conn.QueryContext(ctx, `
		SELECT COALESCE(path, ''), byte_range_length
		FROM packaged_segments
		WHERE package_id = ?
		ORDER BY segment_number`, packageID)
	if err != nil {
		return 0, fmt.Errorf("query segments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		var byteRangeLength sql.NullInt64
		if err := rows.Scan(&path, &byteRangeLength); err != nil {
			return 0, fmt.Errorf("scan segment: %w", err)
		}
		if byteRangeLength.Valid && byteRangeLength.Int64 > 0 {
			total += byteRangeLength.Int64
			continue
		}
		if path == "" {
			return 0, errors.New("segment path is empty")
		}
		n, err := fileSize(path)
		if err != nil {
			return 0, fmt.Errorf("stat segment %q: %w", path, err)
		}
		total += n
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("scan segments: %w", err)
	}
	if total <= 0 {
		return 0, errors.New("tracked files total zero bytes")
	}
	return total, nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if info.IsDir() {
		return 0, fmt.Errorf("is a directory")
	}
	return info.Size(), nil
}

func maintAuditDuration(args []string) {
	fs := flag.NewFlagSet("audit-duration", flag.ExitOnError)
	fix := fs.Bool("fix", false, "requeue truncated packages for re-encode (marks them pending in place)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: linearcast-admin maint audit-duration [--fix]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() != 0 {
		fs.Usage()
		os.Exit(2)
	}

	dbPath := os.Getenv("LINEARCAST_DB")
	if dbPath == "" {
		log.Fatal("LINEARCAST_DB is required")
	}

	open := db.OpenReadOnly
	if *fix {
		open = db.OpenReadWrite
	}
	conn, err := open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	offenders, err := packager.AuditReadyPackageDurations(ctx, conn)
	if err != nil {
		log.Fatalf("audit: %v", err)
	}
	if len(offenders) == 0 {
		fmt.Println("no truncated or unverifiable ready packages found")
		return
	}

	const rowFmt = "%-40s  %-32s  %-22s  %12s  %12s  %12s  %12s\n"
	fmt.Printf(rowFmt, "package", "media", "profile", "packaged_ms", "source_ms", "short_ms", "tol_ms")
	var truncated, unknown int
	for _, o := range offenders {
		if o.UnknownSource {
			unknown++
			fmt.Printf(rowFmt, o.PackageID, o.MediaID, o.Profile, "?", "?", "?", "?")
			continue
		}
		truncated++
		fmt.Printf(rowFmt, o.PackageID, o.MediaID, o.Profile,
			strconv.FormatInt(o.PackagedMs, 10), strconv.FormatInt(o.SourceMs, 10),
			strconv.FormatInt(o.ShortfallMs, 10), strconv.FormatInt(o.ToleranceMs, 10))
	}
	fmt.Printf("\n%d truncated, %d unverifiable (unknown packaged/source duration)\n", truncated, unknown)

	if !*fix {
		if truncated > 0 {
			fmt.Println("\nrun with --fix to requeue the truncated packages for re-encode")
		}
		return
	}

	nowMs := time.Now().UTC().UnixMilli()
	var requeued int
	for _, o := range offenders {
		if o.UnknownSource {
			continue // cannot verify against source; never blindly requeue
		}
		reason := fmt.Sprintf("audit-duration: packaged %dms is %dms short of source %dms; encode likely truncated",
			o.PackagedMs, o.ShortfallMs, o.SourceMs)
		changed, err := db.MarkReadyPackagePendingForReencode(ctx, conn, o.PackageID, nowMs, reason)
		if err != nil {
			log.Fatalf("requeue %s: %v", o.PackageID, err)
		}
		if changed {
			requeued++
			fmt.Printf("requeued: %s (media=%s profile=%s)\n", o.PackageID, o.MediaID, o.Profile)
		}
	}
	fmt.Printf("\nrequeued %d package(s) for re-encode\n", requeued)
	if unknown > 0 {
		fmt.Printf("left %d unverifiable package(s) ready; re-check their media duration metadata\n", unknown)
	}
}

func maintDeleteEncode(args []string) {
	fs := flag.NewFlagSet("delete-encode", flag.ExitOnError)
	profile := fs.String("profile", "", "limit deletion to a single rendition profile")
	force := fs.Bool("force", false, "skip confirmation prompt and override schedule warning")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: linearcast-admin maint delete-encode [--profile <profile>] [--force] <mediaID>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	mediaID := fs.Arg(0)

	dbPath := os.Getenv("LINEARCAST_DB")
	if dbPath == "" {
		log.Fatal("LINEARCAST_DB is required")
	}

	conn, err := db.OpenReadWrite(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	media, err := db.MediaByID(context.Background(), conn, mediaID)
	if err != nil {
		log.Fatalf("query media: %v", err)
	}
	if media == nil {
		log.Fatalf("media not found: %s", mediaID)
	}
	fmt.Printf("media:   %s\npath:    %s\n\n", media.ID, media.Path)

	pkgs, err := db.MediaPackagesForMedia(context.Background(), conn, mediaID)
	if err != nil {
		log.Fatalf("query packages: %v", err)
	}
	var targets []db.MediaPackage
	for _, p := range pkgs {
		if *profile == "" || p.RenditionProfile == *profile {
			targets = append(targets, p)
		}
	}
	if len(targets) == 0 {
		if *profile != "" {
			fmt.Printf("no package found for media %s with profile %q\n", mediaID, *profile)
		} else {
			fmt.Printf("no packages found for media %s\n", mediaID)
		}
		return
	}

	fmt.Println("packages to delete:")
	for _, p := range targets {
		root := "(no root)"
		if p.PackageRoot != nil {
			root = *p.PackageRoot
		}
		fmt.Printf("  [%s]  profile=%-20s  status=%-10s  root=%s\n", p.ID, p.RenditionProfile, p.Status, root)
	}
	fmt.Println()

	nowMs := time.Now().UnixMilli()
	future, err := db.FutureScheduleEntriesForMedia(context.Background(), conn, mediaID, nowMs)
	if err != nil {
		log.Fatalf("query schedule: %v", err)
	}
	if len(future) > 0 {
		fmt.Printf("WARNING: %d future schedule entry(s) reference this media:\n", len(future))
		for _, e := range future {
			t := time.UnixMilli(e.StartMs).UTC()
			fmt.Printf("  channel=%-20s  start=%s\n", e.ChannelID, t.Format(time.RFC3339))
		}
		fmt.Println()
		if !*force {
			log.Fatal("aborting: future schedule entries exist; re-run with --force to proceed anyway")
		}
		fmt.Println("WARNING: proceeding despite future schedule entries (--force)")
	}

	if !*force {
		fmt.Print("Confirm deletion? [y/N]: ")
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("aborted")
			return
		}
	}

	deleted, err := db.DeleteMediaPackagesByMediaID(context.Background(), conn, mediaID, *profile)
	if err != nil {
		log.Fatalf("delete packages: %v", err)
	}

	var diskErrors []string
	for _, p := range deleted {
		if p.PackageRoot == nil || *p.PackageRoot == "" {
			continue
		}
		if err := os.RemoveAll(*p.PackageRoot); err != nil {
			diskErrors = append(diskErrors, fmt.Sprintf("  %s: %v", *p.PackageRoot, err))
		} else {
			fmt.Printf("removed dir: %s\n", *p.PackageRoot)
		}
	}

	fmt.Printf("\ndeleted %d package(s) from db\n", len(deleted))
	if len(diskErrors) > 0 {
		fmt.Fprintln(os.Stderr, "disk cleanup errors (db rows were still deleted):")
		for _, e := range diskErrors {
			fmt.Fprintln(os.Stderr, e)
		}
		os.Exit(1)
	}
}
