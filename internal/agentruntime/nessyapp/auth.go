package nessyapp

import (
	"encoding/json"
	"errors"
	"strings"
)

func validateAuthToken(token string) error {
	if strings.TrimSpace(token) == "" || strings.ContainsRune(token, 0) {
		return errors.New("~/.stepan/settings.json: nessy.auth_token must be a nonempty environment-compatible string")
	}
	return nil
}

// containsCredential also checks decoded strings: JSON escaping must not bypass
// the boundary before a result reaches the application or its documents.
func containsCredential(data []byte, token string) bool {
	if token == "" {
		return false
	}
	if strings.Contains(string(data), token) {
		return true
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return false
	}
	return valueContainsCredential(value, token)
}

func valueContainsCredential(value any, token string) bool {
	switch value := value.(type) {
	case string:
		if strings.Contains(value, token) {
			return true
		}
		// ACP text may itself contain a JSON document.
		var nested any
		if json.Unmarshal([]byte(value), &nested) == nil && nested != nil {
			return valueContainsCredential(nested, token)
		}
	case []any:
		for _, item := range value {
			if valueContainsCredential(item, token) {
				return true
			}
		}
	case map[string]any:
		for key, item := range value {
			if strings.Contains(key, token) || valueContainsCredential(item, token) {
				return true
			}
		}
	}
	return false
}
