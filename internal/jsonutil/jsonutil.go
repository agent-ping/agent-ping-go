// Package jsonutil offers small lazy helpers for reading well-known fields
// out of provider response bodies without fully unmarshalling them.
package jsonutil

import (
	"encoding/json"
	"io"
)

// Decode reads JSON from r into a generic any. Returns nil on error so callers
// can treat absent or malformed bodies the same.
func Decode(r io.Reader) any {
	var v any
	if err := json.NewDecoder(r).Decode(&v); err != nil {
		return nil
	}
	return v
}

// GetInt walks a path of map keys and returns the integer value if present.
// Missing or non-numeric values yield (0, false).
func GetInt(root any, path ...string) (int, bool) {
	v := walk(root, path)
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case int64:
		return int(x), true
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	}
	return 0, false
}

// GetString walks a path and returns the string value if present.
func GetString(root any, path ...string) (string, bool) {
	v := walk(root, path)
	if s, ok := v.(string); ok {
		return s, true
	}
	return "", false
}

func walk(root any, path []string) any {
	cur := root
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
		if cur == nil {
			return nil
		}
	}
	return cur
}
