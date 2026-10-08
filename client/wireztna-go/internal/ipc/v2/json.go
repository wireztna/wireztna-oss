package v2

import "encoding/json"

// Kept behind a package function so all outbound payload helpers use the same
// standard-library JSON implementation as framed envelopes.
func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}
