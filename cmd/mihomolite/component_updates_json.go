package main

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
)

// Duplicate keys (including encoding/json's case-insensitive aliases) are never
// allowed to select a different component, digest, action, or manifest identity.
// Unknown upstream GitHub properties remain compatible; exact schema validation
// is used only for our local update journals and application requests.
func componentJSONUnique(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return errComponentTrust
		}
		token, err := decoder.Token()
		if err != nil {
			return errComponentTrust
		}
		delim, container := token.(json.Delim)
		if !container {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, e := decoder.Token()
				if e != nil {
					return errComponentTrust
				}
				name, ok := key.(string)
				if !ok {
					return errComponentTrust
				}
				fold := strings.ToLower(name)
				if seen[fold] {
					return errComponentTrust
				}
				seen[fold] = true
				if e = value(depth + 1); e != nil {
					return e
				}
			}
			end, e := decoder.Token()
			if e != nil || end != json.Delim('}') {
				return errComponentTrust
			}
		case '[':
			for decoder.More() {
				if e := value(depth + 1); e != nil {
					return e
				}
			}
			end, e := decoder.Token()
			if e != nil || end != json.Delim(']') {
				return errComponentTrust
			}
		default:
			return errComponentTrust
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errComponentTrust
	}
	return nil
}
func componentJSONExact(data []byte, value any) error {
	if err := componentJSONUnique(data); err != nil {
		return err
	}
	rawType := reflect.TypeOf(json.RawMessage{})
	var fields func(json.RawMessage, reflect.Type) error
	fields = func(raw json.RawMessage, t reflect.Type) error {
		if t == rawType {
			return nil
		}
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Struct:
			var object map[string]json.RawMessage
			if json.Unmarshal(raw, &object) != nil || object == nil {
				return errComponentTrust
			}
			allowed := map[string]reflect.Type{}
			for i := 0; i < t.NumField(); i++ {
				field := t.Field(i)
				fullTag := field.Tag.Get("json")
				tag := strings.Split(fullTag, ",")[0]
				if tag != "" && tag != "-" {
					allowed[tag] = field.Type
					if !strings.Contains(fullTag, ",omitempty") {
						if _, exists := object[tag]; !exists {
							return errComponentTrust
						}
					}
				}
			}
			for key, child := range object {
				childType, ok := allowed[key]
				if !ok {
					return errComponentTrust
				}
				if err := fields(child, childType); err != nil {
					return err
				}
			}
		case reflect.Slice:
			var array []json.RawMessage
			if json.Unmarshal(raw, &array) != nil {
				return errComponentTrust
			}
			for _, child := range array {
				if err := fields(child, t.Elem()); err != nil {
					return err
				}
			}
		default:
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return errComponentTrust
			}
		}
		return nil
	}
	t := reflect.TypeOf(value)
	if t == nil || t.Kind() != reflect.Pointer {
		return errComponentTrust
	}
	if err := fields(data, t.Elem()); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return errComponentTrust
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errComponentTrust
	}
	return nil
}
