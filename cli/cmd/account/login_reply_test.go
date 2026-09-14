package account

import (
	"encoding/json"
	"testing"
)

// THE LOGIN THAT WAS BROKEN IN A SHIPPED RELEASE.
//
// One struct parses both of /login's replies, and the platform sends
// `expires_in` as a different JSON type in each — a number on the second-factor
// challenge, a string on the token pair. With the field declared `int`, every
// token-pair reply failed to parse, so `drift account login` refused every
// account WITHOUT a second factor and blamed the API for it.
//
// The tokens are what the caller needs. This is the assertion that they survive
// whatever shape the field beside them arrives in.
func TestALoginReplyParsesWhateverShapeExpiresInArrivesIn(t *testing.T) {
	for name, body := range map[string]string{
		// What the token pair actually sends: h.JsonOK over a map[string]string,
		// so every value is quoted. This is the one that was broken.
		"a string, as the token pair sends it": `{"access_token":"a","refresh_token":"r","token_type":"Bearer","expires_in":"900"}`,
		// What the challenge sends: a map[string]any, so the number stays a number.
		"a number, as the challenge sends it": `{"access_token":"a","refresh_token":"r","expires_in":900}`,
		// Neither. A field this client does not read must never be able to refuse
		// a session that came with working tokens.
		"null":     `{"access_token":"a","refresh_token":"r","expires_in":null}`,
		"nonsense": `{"access_token":"a","refresh_token":"r","expires_in":"not a number"}`,
		"absent":   `{"access_token":"a","refresh_token":"r"}`,
	} {
		var reply loginReply
		if err := json.Unmarshal([]byte(body), &reply); err != nil {
			t.Errorf("%s: the reply would not parse — every login against a platform "+
				"sending this shape is refused: %v", name, err)
			continue
		}
		if reply.AccessToken != "a" || reply.RefreshToken != "r" {
			t.Errorf("%s: the tokens did not survive the parse: %+v", name, reply)
		}
	}
}

// And it still reads the value when the value is readable — the point is
// tolerance, not discarding the field.
func TestExpiresInIsReadFromEitherShape(t *testing.T) {
	for name, body := range map[string]string{
		"string": `{"expires_in":"900"}`,
		"number": `{"expires_in":900}`,
	} {
		var reply loginReply
		if err := json.Unmarshal([]byte(body), &reply); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if reply.ExpiresIn != 900 {
			t.Errorf("%s: expires_in = %d, want 900", name, reply.ExpiresIn)
		}
	}
}
