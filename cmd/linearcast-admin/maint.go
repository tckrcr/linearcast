package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/tckrcr/linearcast/internal/db"
)

func runMaint(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		maintUsage()
		return
	}
	switch args[0] {
	case "delete-encode":
		maintDeleteEncode(args[1:])
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
