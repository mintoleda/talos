package version

import "strings"

// VERSION is the current talos version. Bump this when the wire protocol
// changes incompatibly.
const VERSION = "0.2.0"

// Compatible reports whether a server version is compatible with this client.
// Major-version mismatches are rejected; minor/patch differences are allowed.
func Compatible(server string) bool {
	if server == "" {
		return false
	}
	ours := strings.Split(VERSION, ".")
	theirs := strings.Split(server, ".")
	if len(ours) == 0 || len(theirs) == 0 {
		return false
	}
	return ours[0] == theirs[0]
}
