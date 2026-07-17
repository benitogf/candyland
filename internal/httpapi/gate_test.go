package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/benitogf/candyland/internal/run"
)

// The launch gate rejects run creation when the resolved gh can't deliver
// (missing the workflow scope), admits it when gh holds repo+workflow, and — the
// fail-open case — admits it when scopes aren't inspectable (fine-grained token).
func TestRunCreateGhLaunchGate(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		wantReject bool
	}{
		{
			name:       "missing workflow → 400",
			status:     "Logged in to github.com\n- Token scopes: 'repo'",
			wantReject: true,
		},
		{
			name:       "repo+workflow → admitted",
			status:     "Logged in to github.com\n- Token scopes: 'repo', 'workflow'",
			wantReject: false,
		},
		{
			name:       "no scopes line → fail open, admitted",
			status:     "Logged in to github.com account x (keyring)\n- Active account: true",
			wantReject: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, srv := questServer(t)
			writeGhAuthStub(t, tc.status) // overrides questServer's default capable stub
			base := "http://" + srv.Address

			resp := post(t, base+"/api/runs", run.Spec{Prompt: "build a thing", Folders: []string{"/repo"}})
			defer resp.Body.Close()

			if tc.wantReject {
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400", resp.StatusCode)
				}
				b, _ := io.ReadAll(resp.Body)
				body := string(b)
				if !strings.Contains(body, "gh auth refresh") {
					t.Fatalf("400 body missing remedy: %q", body)
				}
			} else if resp.StatusCode == http.StatusBadRequest {
				t.Fatalf("status = 400, want non-400 (admitted)")
			}
		})
	}
}
