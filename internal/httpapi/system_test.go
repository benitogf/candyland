package httpapi

import (
	"reflect"
	"testing"
)

// parseGhAuthStatus is PURE and carries the whole capability judgement — the
// launch gate and the system panel both key off it. These cases pin the fail-open
// (uninspectable scopes) and fail-closed (known-missing scope) boundaries and the
// tolerant parse of both gh's quoted and older unquoted scopes forms.
func TestParseGhAuthStatus(t *testing.T) {
	cases := []struct {
		name        string
		out         string
		installed   bool
		wantInstall bool
		wantAuthed  bool
		wantKnown   bool
		wantMissing []string
		wantRemedy  bool
	}{
		{
			name:        "authed quoted repo+workflow → not blocked",
			out:         "Logged in to github.com account x\n- Token scopes: 'gist', 'read:org', 'repo', 'workflow'",
			installed:   true,
			wantInstall: true, wantAuthed: true, wantKnown: true, wantMissing: nil, wantRemedy: false,
		},
		{
			name:        "authed unquoted repo+workflow → tolerant parse, not blocked",
			out:         "Logged in to github.com\nToken scopes: repo, workflow",
			installed:   true,
			wantInstall: true, wantAuthed: true, wantKnown: true, wantMissing: nil, wantRemedy: false,
		},
		{
			name:        "authed missing workflow",
			out:         "Logged in to github.com\n- Token scopes: 'repo'",
			installed:   true,
			wantInstall: true, wantAuthed: true, wantKnown: true, wantMissing: []string{"workflow"}, wantRemedy: true,
		},
		{
			name:        "authed missing both",
			out:         "Logged in to github.com\n- Token scopes: 'gist', 'read:org'",
			installed:   true,
			wantInstall: true, wantAuthed: true, wantKnown: true, wantMissing: []string{"repo", "workflow"}, wantRemedy: true,
		},
		{
			name:        "authed no scopes line → fail open",
			out:         "Logged in to github.com account x (keyring)\n- Active account: true",
			installed:   true,
			wantInstall: true, wantAuthed: true, wantKnown: false, wantMissing: nil, wantRemedy: false,
		},
		{
			name: "multi-account: active has repo+workflow, inactive missing → not blocked",
			out: "github.com\n" +
				"  ✓ Logged in to github.com account USERA (keyring)\n" +
				"  - Active account: true\n" +
				"  - Git operations protocol: https\n" +
				"  - Token: gho_****\n" +
				"  - Token scopes: 'gist', 'read:org', 'repo', 'workflow'\n" +
				"\n" +
				"  ✓ Logged in to github.com account USERB (keyring)\n" +
				"  - Active account: false\n" +
				"  - Token: gho_****\n" +
				"  - Token scopes: 'repo'",
			installed:   true,
			wantInstall: true, wantAuthed: true, wantKnown: true, wantMissing: nil, wantRemedy: false,
		},
		{
			name: "multi-account: active missing workflow, inactive complete → blocked on active",
			out: "github.com\n" +
				"  ✓ Logged in to github.com account USERA (keyring)\n" +
				"  - Active account: false\n" +
				"  - Token: gho_****\n" +
				"  - Token scopes: 'gist', 'read:org', 'repo', 'workflow'\n" +
				"\n" +
				"  ✓ Logged in to github.com account USERB (keyring)\n" +
				"  - Active account: true\n" +
				"  - Token: gho_****\n" +
				"  - Token scopes: 'repo'",
			installed:   true,
			wantInstall: true, wantAuthed: true, wantKnown: true, wantMissing: []string{"workflow"}, wantRemedy: true,
		},
		{
			name: "multi-host: GHE active first, github.com capable second → github.com decides, not blocked",
			out: "ghe.corp.com\n" +
				"  ✓ Logged in to ghe.corp.com account bob (keyring)\n" +
				"  - Active account: true\n" +
				"  - Git operations protocol: https\n" +
				"  - Token: ghe_****\n" +
				"  - Token scopes: 'repo'\n" +
				"\n" +
				"github.com\n" +
				"  ✓ Logged in to github.com account alice (keyring)\n" +
				"  - Active account: true\n" +
				"  - Git operations protocol: https\n" +
				"  - Token: gho_****\n" +
				"  - Token scopes: 'repo', 'workflow'",
			installed:   true,
			wantInstall: true, wantAuthed: true, wantKnown: true, wantMissing: nil, wantRemedy: false,
		},
		{
			name:        "not logged in",
			out:         "You are not logged into any GitHub hosts. Run gh auth login to authenticate.",
			installed:   true,
			wantInstall: true, wantAuthed: false, wantKnown: false, wantMissing: nil, wantRemedy: true,
		},
		{
			name:        "not installed",
			out:         "",
			installed:   false,
			wantInstall: false, wantAuthed: false, wantKnown: false, wantMissing: nil, wantRemedy: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseGhAuthStatus(tc.out, tc.installed)
			if got.Installed != tc.wantInstall {
				t.Errorf("Installed = %v, want %v", got.Installed, tc.wantInstall)
			}
			if got.Authed != tc.wantAuthed {
				t.Errorf("Authed = %v, want %v", got.Authed, tc.wantAuthed)
			}
			if got.ScopesKnown != tc.wantKnown {
				t.Errorf("ScopesKnown = %v, want %v", got.ScopesKnown, tc.wantKnown)
			}
			if !reflect.DeepEqual(got.Missing, tc.wantMissing) {
				t.Errorf("Missing = %v, want %v", got.Missing, tc.wantMissing)
			}
			if (got.Remedy != "") != tc.wantRemedy {
				t.Errorf("Remedy = %q, wantRemedy = %v", got.Remedy, tc.wantRemedy)
			}
		})
	}
}
