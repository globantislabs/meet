package store

import "encoding/json"

// marshalJSON is a tiny indirection so the redis.go file stays import-light.
func marshalJSON(v any) ([]byte, error) { return json.Marshal(v) }
