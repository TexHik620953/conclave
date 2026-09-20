package config

import (
	"encoding/json"
	"reflect"
	"strings"
)

var durationType = reflect.TypeOf(Duration(0))

// Schema returns a JSON Schema describing the configuration structure.
func Schema() ([]byte, error) {
	schema := schemaFor(reflect.TypeOf(Config{}))
	root := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"title":   "conclave configuration",
	}
	for k, v := range schema.(map[string]any) {
		root[k] = v
	}
	return json.MarshalIndent(root, "", "  ")
}

func schemaFor(t reflect.Type) any {
	switch t.Kind() {
	case reflect.Pointer:
		return schemaFor(t.Elem())
	case reflect.Struct:
		if t == durationType {
			return map[string]any{"type": "string", "description": "Go duration, e.g. 30s, 5m."}
		}
		props := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := yamlName(f)
			if name == "" || name == "-" {
				continue
			}
			props[name] = schemaFor(f.Type)
		}
		return map[string]any{"type": "object", "properties": props}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaFor(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaFor(t.Elem())}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	default:
		return map[string]any{}
	}
}

func yamlName(f reflect.StructField) string {
	tag := f.Tag.Get("yaml")
	if tag == "" {
		return ""
	}
	name := strings.Split(tag, ",")[0]
	if name == "" {
		return strings.ToLower(f.Name)
	}
	return name
}
