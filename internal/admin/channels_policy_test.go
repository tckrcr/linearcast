package admin

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

// These characterize the exact JSON wire shape of GET channel policy for the
// two states of the nullable Channel fields it exposes:
//   - required_package_profile NULL  -> falls back to the media-kind default
//   - package_prefill_ms       NULL  -> serialized as JSON null
//   - both set                       -> echoed through verbatim
//
// They are byte-stable across the A1 sql.Null* de-leak: the handler signature
// and channelPolicyResponse are unchanged, so the body must stay identical when
// Channel.RequiredPackageProfile/PackagePrefillMs flip from sql.Null* to
// string / *int64.

func policyRequest(app *App, channelID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/channels/"+channelID+"/policy", nil)
	req.SetPathValue("channelID", channelID)
	res := httptest.NewRecorder()
	app.handleChannelPolicy(res, req)
	return res
}

func TestHandleChannelPolicyNullFieldsWireShape(t *testing.T) {
	app, conn := testAdminApp(t)
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms, playback_mode
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	res := policyRequest(app, "ch")
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}

	defaultProfile := db.DefaultPackageProfileForMediaKind(db.MediaKindVideo)
	want := fmt.Sprintf(
		`{"channelId":"ch","playbackMode":"packaged","requiredPackageProfile":%q,"adaptiveBitrate":false,"packagePrefillMs":null,"mediaKind":"video"}`+"\n",
		defaultProfile,
	)
	if got := res.Body.String(); got != want {
		t.Fatalf("policy body mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestHandleChannelPolicySetFieldsWireShape(t *testing.T) {
	app, conn := testAdminApp(t)
	if _, err := conn.Exec(`INSERT INTO channels (
			id, display_name, source_directory, ordering, enabled, created_at_ms,
			playback_mode, required_package_profile, package_prefill_ms
		)
		VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, 'packaged', 'h264-1080p-8mbps', 5000)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	res := policyRequest(app, "ch")
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}

	want := `{"channelId":"ch","playbackMode":"packaged","requiredPackageProfile":"h264-1080p-8mbps","adaptiveBitrate":false,"packagePrefillMs":5000,"mediaKind":"video"}` + "\n"
	if got := res.Body.String(); got != want {
		t.Fatalf("policy body mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestHandleChannelPolicyUpdateRejectsProfileChange(t *testing.T) {
	app, conn := testAdminApp(t)
	insertDeleteFixture(t, conn, true)

	profile, ok := packageprofile.Lookup(packageprofile.DefaultName)
	if !ok {
		t.Fatal("default profile missing")
	}
	profile.Name = "custom-h264-1080p"
	profile.Label = "Custom 1080p"
	if err := db.UpsertPackageProfile(context.Background(), conn, profile); err != nil {
		t.Fatalf("insert custom profile: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/channels/ch/policy",
		bytes.NewBufferString(`{"requiredPackageProfile":"custom-h264-1080p"}`))
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()

	app.handleChannelPolicyUpdate(res, req)

	if res.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want conflict", res.Code, res.Body.String())
	}
	if code := errorCode(t, res); code != "unsupported_policy_update" {
		t.Fatalf("error code=%q, want unsupported_policy_update", code)
	}
	ch, err := db.ChannelByID(context.Background(), conn, "ch")
	if err != nil {
		t.Fatalf("lookup channel: %v", err)
	}
	if ch == nil || ch.RequiredPackageProfile != "h264-1080p-8mbps" {
		t.Fatalf("channel profile changed: %+v", ch)
	}
}
