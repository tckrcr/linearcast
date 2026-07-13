package admin

import (
	"fmt"
	"net/http"

	"github.com/tckrcr/linearcast/internal/db"
)

type mediaDeleteBlocker struct {
	ChannelID   string `json:"channelId"`
	DisplayName string `json:"displayName"`
	Kind        string `json:"kind"`
}

type mediaDeleteResponse struct {
	MediaID   string               `json:"mediaId"`
	Deleted   bool                 `json:"deleted"`
	Blockers  []mediaDeleteBlocker `json:"blockers,omitempty"`
	Warnings  []string             `json:"warnings,omitempty"`
	PackageID []string             `json:"packageIds,omitempty"`
}

func (a *App) handleMediaDelete(w http.ResponseWriter, r *http.Request) {
	mediaID := r.PathValue("mediaID")

	m, err := db.MediaByID(r.Context(), a.dbConn, mediaID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if m == nil {
		writeError(w, http.StatusNotFound, "not_found", "media not found")
		return
	}

	blockers, err := db.ActiveMediaDeleteBlockers(r.Context(), a.dbConn, mediaID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if len(blockers) > 0 {
		resp := mediaDeleteResponse{
			MediaID:  mediaID,
			Deleted:  false,
			Blockers: make([]mediaDeleteBlocker, 0, len(blockers)),
		}
		for _, blocker := range blockers {
			resp.Blockers = append(resp.Blockers, mediaDeleteBlocker{
				ChannelID:   blocker.ChannelID,
				DisplayName: blocker.DisplayName,
				Kind:        blocker.Kind,
			})
		}
		writeJSONStatus(w, http.StatusConflict, resp)
		return
	}

	pkgs, err := db.MediaPackagesForMedia(r.Context(), a.dbConn, mediaID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	resp := mediaDeleteResponse{
		MediaID:   mediaID,
		Deleted:   false,
		Blockers:  []mediaDeleteBlocker{},
		Warnings:  []string{},
		PackageID: make([]string, 0, len(pkgs)),
	}
	for _, pkg := range pkgs {
		resp.PackageID = append(resp.PackageID, pkg.ID)
		if pkg.PackageRoot == nil || *pkg.PackageRoot == "" {
			continue
		}
		if err := deletePackageContents(*pkg.PackageRoot); err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("disk cleanup %s: %v", *pkg.PackageRoot, err))
		}
	}

	deleted, err := db.DeleteMediaMetadataByID(r.Context(), a.dbConn, mediaID)
	if err != nil {
		blockers, blockerErr := db.ActiveMediaDeleteBlockers(r.Context(), a.dbConn, mediaID)
		if blockerErr == nil && len(blockers) > 0 {
			resp.Blockers = resp.Blockers[:0]
			for _, blocker := range blockers {
				resp.Blockers = append(resp.Blockers, mediaDeleteBlocker{
					ChannelID:   blocker.ChannelID,
					DisplayName: blocker.DisplayName,
					Kind:        blocker.Kind,
				})
			}
			writeJSONStatus(w, http.StatusConflict, resp)
			return
		}
		writeError(w, http.StatusConflict, "delete_blocked", err.Error())
		return
	}
	resp.Deleted = deleted
	writeJSON(w, resp)
}
