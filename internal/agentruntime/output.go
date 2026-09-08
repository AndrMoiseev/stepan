package agentruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"regexp"
	"unicode/utf8"
)

// ValidateOutput validates provider structured output against the immutable
// schema attached to its logical thread. It implements the deliberately small
// JSON Schema subset used by Stepan's response contracts. Providers may use a
// wider connection-level schema, but a turn never escapes its thread schema.
func ValidateOutput(schema, output json.RawMessage) error {
	root, err := decodeJSONValue(schema)
	if err != nil {
		return fmt.Errorf("invalid output schema: %w", err)
	}
	rootObject, ok := root.(map[string]any)
	if !ok {
		return errors.New("invalid output schema: root must be an object")
	}
	value, err := decodeJSONValue(output)
	if err != nil {
		return fmt.Errorf("invalid structured output: %w", err)
	}
	if err := validateJSONValue(rootObject, rootObject, value, "$", 0); err != nil {
		return fmt.Errorf("structured output does not match schema: %w", err)
	}
	return nil
}

func decodeJSONValue(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing JSON")
	}
	return value, nil
}

func validateJSONValue(root, schema map[string]any, value any, path string, depth int) error {
	if depth > 64 {
		return errors.New("schema nesting exceeds 64 levels")
	}
	if reference, ok := schema["$ref"].(string); ok {
		const prefix = "#/$defs/"
		if len(reference) <= len(prefix) || reference[:len(prefix)] != prefix {
			return fmt.Errorf("%s uses unsupported reference %q", path, reference)
		}
		definitions, ok := root["$defs"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s references missing definitions", path)
		}
		referenced, ok := definitions[reference[len(prefix):]].(map[string]any)
		if !ok {
			return fmt.Errorf("%s references unknown definition %q", path, reference)
		}
		return validateJSONValue(root, referenced, value, path, depth+1)
	}
	if alternatives, ok := schema["oneOf"].([]any); ok {
		matches := 0
		for _, candidate := range alternatives {
			object, ok := candidate.(map[string]any)
			if ok && validateJSONValue(root, object, value, path, depth+1) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s matches %d oneOf alternatives", path, matches)
		}
		return nil
	}
	if values, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range values {
			if reflect.DeepEqual(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is not an allowed enum value", path)
		}
	}
	typeName, _ := schema["type"].(string)
	switch typeName {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		properties, _ := schema["properties"].(map[string]any)
		required := make(map[string]bool)
		if names, ok := schema["required"].([]any); ok {
			for _, item := range names {
				name, ok := item.(string)
				if !ok {
					return fmt.Errorf("%s has a non-string required property", path)
				}
				required[name] = true
			}
		}
		for name := range required {
			if _, exists := object[name]; !exists {
				return fmt.Errorf("%s.%s is required", path, name)
			}
		}
		additional, disallowAdditional := schema["additionalProperties"].(bool)
		for name, item := range object {
			property, exists := properties[name]
			if !exists {
				if disallowAdditional && !additional {
					return fmt.Errorf("%s.%s is not allowed", path, name)
				}
				continue
			}
			propertySchema, ok := property.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.%s has an invalid property schema", path, name)
			}
			if err := validateJSONValue(root, propertySchema, item, path+"."+name, depth+1); err != nil {
				return err
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for index, item := range items {
				if err := validateJSONValue(root, itemSchema, item, fmt.Sprintf("%s[%d]", path, index), depth+1); err != nil {
					return err
				}
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		length := utf8.RuneCountInString(text)
		if minimum, ok := schema["minLength"].(json.Number); ok {
			limit, err := numberAsInt(minimum)
			if err != nil || int64(length) < limit {
				return fmt.Errorf("%s is shorter than minLength", path)
			}
		}
		if maximum, ok := schema["maxLength"].(json.Number); ok {
			limit, err := numberAsInt(maximum)
			if err != nil || int64(length) > limit {
				return fmt.Errorf("%s is longer than maxLength", path)
			}
		}
		if pattern, ok := schema["pattern"].(string); ok {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("%s has invalid pattern: %w", path, err)
			}
			if !compiled.MatchString(text) {
				return fmt.Errorf("%s does not match pattern", path)
			}
		}
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return fmt.Errorf("%s must be an integer", path)
		}
		integer := new(big.Int)
		if _, ok := integer.SetString(string(number), 10); !ok {
			return fmt.Errorf("%s must be an integer", path)
		}
		if minimum, ok := schema["minimum"].(json.Number); ok {
			limit := new(big.Int)
			if _, ok := limit.SetString(string(minimum), 10); !ok || integer.Cmp(limit) < 0 {
				return fmt.Errorf("%s is below minimum", path)
			}
		}
	case "":
		// An empty schema is intentionally permissive.
	default:
		return fmt.Errorf("%s uses unsupported schema type %q", path, typeName)
	}
	return nil
}

func numberAsInt(value json.Number) (int64, error) { return value.Int64() }
