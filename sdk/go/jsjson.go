package raildrop

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

var jsIntegerKeyPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]*)$`)

const jsMaxSafeInteger = int64(1) << 53

const jsMaxArrayIndex = uint64(4294967294)

var (
	rawMessageType    = reflect.TypeOf(json.RawMessage(nil))
	jsonNumberType    = reflect.TypeOf(json.Number(""))
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
)

func MarshalJSONJavaScript(value any) (json.RawMessage, error) {
	var builder strings.Builder
	if err := appendJavaScriptJSON(&builder, reflect.ValueOf(value)); err != nil {
		return nil, err
	}
	return json.RawMessage(builder.String()), nil
}

func normalizeJSONJavaScript(value []byte) (json.RawMessage, error) {
	if len(value) == 0 {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return nil, WrapError(CodeBadRequest, "Metadata is not valid JSON.", err)
	}
	return MarshalJSONJavaScript(decoded)
}

func appendJavaScriptJSON(builder *strings.Builder, value reflect.Value) error {
	if value.IsValid() && value.Type() == rawMessageType {
		if value.IsNil() {
			builder.WriteString("null")
			return nil
		}
		normalized, err := normalizeJSONJavaScript(value.Bytes())
		if err != nil {
			return err
		}
		builder.Write(normalized)
		return nil
	}
	if value.IsValid() && value.Type() == jsonNumberType {
		builder.WriteString(value.String())
		return nil
	}
	if marshaler, ok := marshalerFor(value); ok {
		output, err := marshaler.MarshalJSON()
		if err != nil {
			return WrapError(CodeBadRequest, "Custom JSON marshaler failed.", err)
		}
		normalized, err := normalizeJSONJavaScript(output)
		if err != nil {
			return err
		}
		builder.Write(normalized)
		return nil
	}
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			builder.WriteString("null")
			return nil
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		builder.WriteString("null")
		return nil
	}
	switch value.Kind() {
	case reflect.Bool:
		if value.Bool() {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case reflect.String:
		appendJavaScriptString(builder, value.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		emitJSInteger(builder, value.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		emitJSUnsignedInteger(builder, value.Uint())
	case reflect.Float32, reflect.Float64:
		encoded, err := json.Marshal(value.Float())
		if err != nil {
			return err
		}
		builder.Write(encoded)
	case reflect.Slice:
		if value.IsNil() {
			builder.WriteString("null")
			return nil
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			encoded, err := json.Marshal(value.Bytes())
			if err != nil {
				return err
			}
			builder.Write(encoded)
			return nil
		}
		return appendJavaScriptArray(builder, value)
	case reflect.Array:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			bytes := make([]byte, value.Len())
			for index := 0; index < value.Len(); index++ {
				bytes[index] = byte(value.Index(index).Uint())
			}
			encoded, err := json.Marshal(bytes)
			if err != nil {
				return err
			}
			builder.Write(encoded)
			return nil
		}
		return appendJavaScriptArray(builder, value)
	case reflect.Map:
		if value.IsNil() {
			builder.WriteString("null")
			return nil
		}
		return appendJavaScriptMap(builder, value)
	case reflect.Struct:
		return appendJavaScriptStruct(builder, value)
	default:
		return fmt.Errorf("raildrop: metadata value of type %s is not JSON-serializable", value.Type())
	}
	return nil
}

func marshalerFor(value reflect.Value) (json.Marshaler, bool) {
	if !value.IsValid() || !value.CanInterface() {
		return nil, false
	}
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil, false
	}
	if value.Type().Implements(jsonMarshalerType) {
		marshaler, _ := value.Interface().(json.Marshaler)
		return marshaler, true
	}
	if value.CanAddr() && value.Addr().Type().Implements(jsonMarshalerType) {
		marshaler, _ := value.Addr().Interface().(json.Marshaler)
		return marshaler, true
	}
	return nil, false
}

func emitJSInteger(builder *strings.Builder, value int64) {
	if value > jsMaxSafeInteger || value < -jsMaxSafeInteger {
		encoded, _ := json.Marshal(float64(value))
		builder.Write(encoded)
		return
	}
	builder.WriteString(strconv.FormatInt(value, 10))
}

func emitJSUnsignedInteger(builder *strings.Builder, value uint64) {
	if value > uint64(jsMaxSafeInteger) {
		encoded, _ := json.Marshal(float64(value))
		builder.Write(encoded)
		return
	}
	builder.WriteString(strconv.FormatUint(value, 10))
}

func appendJavaScriptArray(builder *strings.Builder, value reflect.Value) error {
	builder.WriteByte('[')
	for index := 0; index < value.Len(); index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		if err := appendJavaScriptJSON(builder, value.Index(index)); err != nil {
			return err
		}
	}
	builder.WriteByte(']')
	return nil
}

type jsEntry struct {
	key   string
	value reflect.Value
}

func appendJavaScriptEntries(builder *strings.Builder, entries []jsEntry) error {
	integerKeys := make([]jsEntry, 0, len(entries))
	otherKeys := make([]jsEntry, 0, len(entries))
	for _, entry := range entries {
		if isJSIntegerKey(entry.key) {
			integerKeys = append(integerKeys, entry)
		} else {
			otherKeys = append(otherKeys, entry)
		}
	}
	sort.SliceStable(integerKeys, func(left int, right int) bool {
		return jsIntegerValue(integerKeys[left].key) < jsIntegerValue(integerKeys[right].key)
	})
	ordered := append(integerKeys, otherKeys...)
	builder.WriteByte('{')
	for index, entry := range ordered {
		if index > 0 {
			builder.WriteByte(',')
		}
		appendJavaScriptString(builder, entry.key)
		builder.WriteByte(':')
		if err := appendJavaScriptJSON(builder, entry.value); err != nil {
			return err
		}
	}
	builder.WriteByte('}')
	return nil
}

func appendJavaScriptMap(builder *strings.Builder, value reflect.Value) error {
	entries := make([]jsEntry, 0, value.Len())
	for _, key := range value.MapKeys() {
		entries = append(entries, jsEntry{key: fmt.Sprintf("%v", key.Interface()), value: value.MapIndex(key)})
	}
	sort.SliceStable(entries, func(left int, right int) bool {
		return entries[left].key < entries[right].key
	})
	return appendJavaScriptEntries(builder, entries)
}

type structField struct {
	name      string
	omitEmpty bool
	parent    reflect.Value
	index     int
}

func collectStructFields(value reflect.Value, fields *[]structField, seen map[string]bool) {
	structType := value.Type()
	for index := 0; index < structType.NumField(); index++ {
		field := structType.Field(index)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		if !field.IsExported() && !field.Anonymous {
			continue
		}
		name := field.Name
		omitEmpty := false
		if tag != "" {
			parts := strings.Split(tag, ",")
			if parts[0] != "" {
				name = parts[0]
			}
			for _, option := range parts[1:] {
				if option == "omitempty" {
					omitEmpty = true
				}
			}
		}
		fieldValue := value.Field(index)
		if field.Anonymous && tag == "" && isStructKind(field.Type) {
			if field.Type.Kind() == reflect.Pointer {
				if fieldValue.IsNil() {
					continue
				}
				collectStructFields(fieldValue.Elem(), fields, seen)
			} else {
				collectStructFields(fieldValue, fields, seen)
			}
			continue
		}
		if !field.IsExported() {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		*fields = append(*fields, structField{name: name, omitEmpty: omitEmpty, parent: value, index: index})
	}
}

func isStructKind(fieldType reflect.Type) bool {
	if fieldType.Kind() == reflect.Struct {
		return true
	}
	return fieldType.Kind() == reflect.Pointer && fieldType.Elem().Kind() == reflect.Struct
}

func isEmptyJSONValue(value reflect.Value) bool {
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return true
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Slice, reflect.Map, reflect.String, reflect.Array:
		return value.Len() == 0
	default:
		return false
	}
}

func appendJavaScriptStruct(builder *strings.Builder, value reflect.Value) error {
	fields := make([]structField, 0, value.NumField())
	collectStructFields(value, &fields, make(map[string]bool))
	entries := make([]jsEntry, 0, len(fields))
	for _, field := range fields {
		fieldValue := field.parent.Field(field.index)
		if field.omitEmpty && isEmptyJSONValue(fieldValue) {
			continue
		}
		entries = append(entries, jsEntry{key: field.name, value: fieldValue})
	}
	return appendJavaScriptEntries(builder, entries)
}

func isJSIntegerKey(key string) bool {
	if !jsIntegerKeyPattern.MatchString(key) || len(key) > 10 {
		return false
	}
	value, err := strconv.ParseUint(key, 10, 64)
	return err == nil && value <= jsMaxArrayIndex
}

func jsIntegerValue(key string) uint64 {
	value, _ := strconv.ParseUint(key, 10, 64)
	return value
}

func appendJavaScriptString(builder *strings.Builder, value string) {
	builder.WriteByte('"')
	for index := 0; index < len(value); {
		character, width := utf8.DecodeRuneInString(value[index:])
		if character == utf8.RuneError && width == 1 {
			builder.WriteString("\ufffd")
			index++
			continue
		}
		switch character {
		case '"':
			builder.WriteString("\\\"")
		case '\\':
			builder.WriteString("\\\\")
		case '\b':
			builder.WriteString("\\b")
		case '\f':
			builder.WriteString("\\f")
		case '\n':
			builder.WriteString("\\n")
		case '\r':
			builder.WriteString("\\r")
		case '\t':
			builder.WriteString("\\t")
		default:
			if character < 0x20 {
				builder.WriteString(fmt.Sprintf("\\u%04x", character))
			} else {
				builder.WriteString(value[index : index+width])
			}
		}
		index += width
	}
	builder.WriteByte('"')
}
