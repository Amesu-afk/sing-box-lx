//go:build darwin || linux || windows

package libbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const redactedCredential = "[REDACTED]"

var sensitiveCredentialKeys = map[string]struct{}{
	"uuid":                 {},
	"password":             {},
	"psk":                  {},
	"privatekey":           {},
	"encryption":           {},
	"authkey":              {},
	"privatekeypassphrase": {},
	"token":                {},
	"secret":               {},
	"apikey":               {},
	"authorization":        {},
	"cookie":               {},
}

// redactConfigCredentials parses the snapshot as a JSON tree and replaces
// credential values, rather than deleting their fields. Tags, addresses,
// protocol structure and non-sensitive headers therefore remain useful in a
// report. Invalid JSON is rejected: copying it verbatim would defeat the
// security boundary on exactly the malformed configurations that need a
// report most.
func redactConfigCredentials(content []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, err
	}
	redactCredentialValue(value, false)
	return json.MarshalIndent(value, "", "  ")
}

func redactCredentialValue(value any, headerMap bool) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			normalized := normalizeCredentialKey(key)
			if _, sensitive := sensitiveCredentialKeys[normalized]; sensitive {
				value[key] = redactedCredential
				continue
			}
			if headerMap && sensitiveCredentialHeader(normalized) {
				value[key] = redactedCredential
				continue
			}
			redactCredentialValue(child, normalized == "header" || normalized == "headers" || strings.HasSuffix(normalized, "headers"))
		}
	case []any:
		for _, child := range value {
			redactCredentialValue(child, headerMap)
		}
	}
}

func normalizeCredentialKey(key string) string {
	var normalized strings.Builder
	for _, char := range strings.ToLower(key) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			normalized.WriteRune(char)
		}
	}
	return normalized.String()
}

func sensitiveCredentialHeader(key string) bool {
	for _, sensitive := range []string{"authorization", "authentication", "cookie", "apikey", "token", "secret", "password"} {
		if strings.Contains(key, sensitive) {
			return true
		}
	}
	return false
}
