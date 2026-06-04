package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

// startupBatFilename is the script we drop into the user's Startup folder so
// the encoder runs at logon without requiring admin elevation. Using the
// Startup folder (instead of Task Scheduler) keeps install working for
// non-admin operators and for unattended SSH-driven installs.
const startupBatFilename = "linearcast-encoder.bat"

func runInstall(ctx context.Context, client *http.Client, cfg config, out io.Writer) error {
	if runtime.GOOS != "windows" {
		return errors.New("install is only supported on Windows; use systemd (Linux) or launchd (macOS) instead")
	}
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine executable path: %w", err)
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return fmt.Errorf("cannot resolve executable path: %w", err)
	}
	exeDir := filepath.Dir(exePath)

	batPath, err := startupBatPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(batPath), 0o755); err != nil {
		return fmt.Errorf("create startup dir: %w", err)
	}
	bat := fmt.Sprintf("@echo off\r\n"+
		"set LINEARCAST_ADMIN_URL=%s\r\n"+
		"set LINEARCAST_ENCODER_API_KEY=%s\r\n"+
		"set LINEARCAST_ENCODER_WORK_DIR=%s\r\n"+
		"cd /d \"%s\"\r\n"+
		"start \"Linearcast Encoder\" /min \"%s\" run\r\n",
		cfg.AdminURL, cfg.APIKey, cfg.WorkDir, exeDir, exePath)
	if err := os.WriteFile(batPath, []byte(bat), 0o644); err != nil {
		return fmt.Errorf("write startup script: %w", err)
	}

	fmt.Fprintf(out, "installed startup script: %s\n", batPath)
	fmt.Fprintln(out, "encoder will start automatically at next interactive logon")
	fmt.Fprintln(out, "to start now without logging out, double-click the script from a desktop session")
	return nil
}

func runUninstall(ctx context.Context, client *http.Client, cfg config, out io.Writer) error {
	if runtime.GOOS != "windows" {
		return errors.New("uninstall is only supported on Windows")
	}
	batPath, err := startupBatPath()
	if err != nil {
		return err
	}
	if err := os.Remove(batPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove startup script: %w", err)
	}
	fmt.Fprintf(out, "removed startup script: %s\n", batPath)
	fmt.Fprintln(out, "note: if the encoder is currently running, end it via Task Manager")
	return nil
}

func startupBatPath() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", errors.New("APPDATA is not set; cannot locate the Startup folder")
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", startupBatFilename), nil
}
