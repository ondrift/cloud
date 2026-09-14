package account

import (
	"strings"
	"testing"
)

// The CLI must not OFFER a scope the platform refuses to mint.
//
// auth rejects account:write and account:delete at mint time, because a token
// carrying either could mint another token — undoing every limit the owner set —
// or end the account it was given to deploy into. A `--scope` list that advertised
// them would send a user to a 403 for using the command exactly as documented,
// which is the dead end this mirror exists to prevent.
//
// The platform's refusal is the one that MATTERS and is tested where it lives
// (auth/routes/token_test.go). This is the menu agreeing with it.
func TestTheScopeMenuNeverOffersWhatThePlatformRefusesToMint(t *testing.T) {
	forbidden := map[string]string{
		"account:write":  "a token carrying it could mint another token",
		"account:delete": "a token carrying it could end the account",
	}
	for _, s := range mintableScopes {
		if why, bad := forbidden[s.name]; bad {
			t.Errorf("--scope offers %s, which auth refuses to mint: %s", s.name, why)
		}
	}
}

// Every offered scope is one the platform defines. A typo here is a menu entry
// that 400s with "unknown scope", reached by following the CLI's own help.
func TestEveryOfferedScopeIsOneThePlatformDefines(t *testing.T) {
	// The platform's vocabulary, from drift-common/scope. Written out rather than
	// imported because the CLI deliberately carries no drift-common dependency —
	// it is `go install`able and must not pull in core code.
	known := map[string]bool{
		"slice:read": true, "slice:write": true,
		"secret:read": true, "secret:write": true,
		"account:read": true, "account:write": true, "account:delete": true,
	}
	for _, s := range mintableScopes {
		if !known[s.name] {
			t.Errorf("--scope offers %q, which is not a scope the platform defines", s.name)
		}
	}
}

// The menu has to be non-empty and self-describing: it is generated into
// --scope's help, and an entry with no explanation is a permission granted by
// someone who could not tell what it did.
func TestTheScopeMenuExplainsEveryEntry(t *testing.T) {
	if len(mintableScopes) == 0 {
		t.Fatal("no mintable scopes — `--scope` would accept nothing and the command could mint no token")
	}
	for _, s := range mintableScopes {
		if strings.TrimSpace(s.does) == "" {
			t.Errorf("%s has no description, so --scope's help cannot say what granting it means", s.name)
		}
	}
	help := scopeHelp()
	for _, s := range mintableScopes {
		if !strings.Contains(help, s.name) {
			t.Errorf("scopeHelp() omits %s", s.name)
		}
	}
}
