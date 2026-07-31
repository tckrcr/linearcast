package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type recordingPlaybackControl struct {
	channelID string
}

func (c *recordingPlaybackControl) StopOnDemandEncoding(channelID string) {
	c.channelID = channelID
}

func TestHandleChannelStopEncoderUsesPlaybackControl(t *testing.T) {
	app, _ := testAdminApp(t)
	control := &recordingPlaybackControl{}
	app.playbackControl = control

	req := httptest.NewRequest(http.MethodPost, "/api/channels/ch/stop-encoder", nil)
	req.SetPathValue("channelID", "ch")
	res := httptest.NewRecorder()
	app.handleChannelStopEncoder(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if control.channelID != "ch" {
		t.Fatalf("controlled channel=%q, want ch", control.channelID)
	}
}
