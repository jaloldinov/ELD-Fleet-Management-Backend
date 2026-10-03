package validation

import (
	"net/mail"
	"strings"
	"time"
)

// emailMaxLen is the practical maximum length of an email address.
const emailMaxLen = 254

// Email accepts a plain address like "jane@acme.com". mail.ParseAddress
// also accepts forms like "Jane <jane@acme.com>", so we require the parsed
// address to equal the input. The domain must contain a dot.
func Email(email string) bool {
	if len(email) > emailMaxLen {
		return false
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return false
	}
	at := strings.LastIndex(email, "@")
	return strings.Contains(email[at+1:], ".")
}

// Timezone accepts IANA names like "America/Chicago" or "UTC".
// time.LoadLocation treats "" as UTC and "Local" as the server's zone, so both
// are rejected explicitly: the zone must not depend on where the server runs.
func Timezone(tz string) bool {
	if tz == "" || tz == "Local" {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

// usStates: the 50 states + DC.
var usStates = map[string]bool{
	"AL": true, "AK": true, "AZ": true, "AR": true, "CA": true, "CO": true, "CT": true, "DE": true,
	"DC": true, "FL": true, "GA": true, "HI": true, "ID": true, "IL": true, "IN": true, "IA": true,
	"KS": true, "KY": true, "LA": true, "ME": true, "MD": true, "MA": true, "MI": true, "MN": true,
	"MS": true, "MO": true, "MT": true, "NE": true, "NV": true, "NH": true, "NJ": true, "NM": true,
	"NY": true, "NC": true, "ND": true, "OH": true, "OK": true, "OR": true, "PA": true, "RI": true,
	"SC": true, "SD": true, "TN": true, "TX": true, "UT": true, "VT": true, "VA": true, "WA": true,
	"WV": true, "WI": true, "WY": true,
}

// USState reports whether code is an upper-case two-letter code of one of
// the 50 US states or DC.
func USState(code string) bool {
	return usStates[code]
}
