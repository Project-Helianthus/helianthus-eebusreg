package eebusraw

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	typedValueMaximumDepth        = 4
	canonicalDocumentMaximumDepth = 16
	typedValueMaximumMembers      = 256
	typedValueMaximumStringBytes  = 16384
	typedValueMaximumKeyRunes     = 256
	typedValueMaximumJCSBytes     = 1048576
	typedValueMaximumSafeInteger  = int64(9007199254740991)
)

var ErrSecretDetected = errors.New("secret-classified raw value rejected")

var rawFeatureSecretNames = map[string]struct{}{
	"private_key":            {},
	"private_pem":            {},
	"trust" + "_store_bytes": {},
	"credential_token":       {},
	"bearer_token":           {},
	"session_token":          {},
	"authentication_token":   {},
	"cryptographic_secret":   {},
}

type HashV1 string

type TypedValueV1 struct {
	value any
}

func NewTypedValueV1(value any) (TypedValueV1, error) {
	canonical, err := canonicalTypedValueV1(value, 1)
	if err != nil {
		return TypedValueV1{}, err
	}
	result := TypedValueV1{value: canonical}
	encoded, err := result.canonicalJSON()
	if err != nil {
		return TypedValueV1{}, err
	}
	if len(encoded) > typedValueMaximumJCSBytes {
		return TypedValueV1{}, errors.New("typed value exceeds the canonical byte limit")
	}
	return result, nil
}

func DecodeTypedValueV1(data []byte) (TypedValueV1, error) {
	decoded, err := decodeTypedJSONV1(data)
	if err != nil {
		return TypedValueV1{}, err
	}
	return NewTypedValueV1(decoded)
}

func CanonicalSHA256V1(value any) (HashV1, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", errors.New("canonical value cannot be encoded")
	}
	decoded, err := decodeTypedJSONV1WithDepth(encoded, canonicalDocumentMaximumDepth)
	if err != nil {
		return "", err
	}
	canonical, err := canonicalTypedValueV1WithDepth(decoded, 1, canonicalDocumentMaximumDepth)
	if err != nil {
		return "", err
	}
	var output bytes.Buffer
	if err := appendCanonicalJSONV1(&output, canonical); err != nil {
		return "", err
	}
	sum := sha256.Sum256(output.Bytes())
	return HashV1("sha256:" + hex.EncodeToString(sum[:])), nil
}

func (value TypedValueV1) Value() any {
	return cloneTypedValueV1(value.value)
}

func (value TypedValueV1) Clone() TypedValueV1 {
	return TypedValueV1{value: cloneTypedValueV1(value.value)}
}

func (value TypedValueV1) Validate() error {
	_, err := NewTypedValueV1(value.value)
	return err
}

func (value TypedValueV1) ComputeHash() (HashV1, error) {
	encoded, err := value.canonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return HashV1("sha256:" + hex.EncodeToString(sum[:])), nil
}

func (value TypedValueV1) MarshalJSON() ([]byte, error) {
	return value.canonicalJSON()
}

func (value *TypedValueV1) UnmarshalJSON(data []byte) error {
	decoded, err := DecodeTypedValueV1(data)
	if err != nil {
		return err
	}
	*value = decoded
	return nil
}

func (value TypedValueV1) String() string {
	return "typed_value_v1:[redacted]"
}

func (value TypedValueV1) GoString() string {
	return value.String()
}

func (value TypedValueV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, value.String())
}

func (value TypedValueV1) canonicalJSON() ([]byte, error) {
	canonical, err := canonicalTypedValueV1(value.value, 1)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := appendCanonicalJSONV1(&output, canonical); err != nil {
		return nil, err
	}
	if output.Len() > typedValueMaximumJCSBytes {
		return nil, errors.New("typed value exceeds the canonical byte limit")
	}
	return output.Bytes(), nil
}

func canonicalTypedValueV1(value any, depth int) (any, error) {
	return canonicalTypedValueV1WithDepth(value, depth, typedValueMaximumDepth)
}

func canonicalTypedValueV1WithDepth(value any, depth int, maximumDepth int) (any, error) {
	if typed, ok := value.(TypedValueV1); ok {
		return canonicalTypedValueV1WithDepth(typed.value, depth, maximumDepth)
	}
	if number, ok := value.(json.Number); ok {
		integer, err := strconv.ParseInt(string(number), 10, 64)
		if err != nil {
			return nil, errors.New("typed numbers must be safe integers or exact strings")
		}
		return canonicalTypedValueV1WithDepth(integer, depth, maximumDepth)
	}
	if value == nil {
		return nil, nil
	}
	reflected := reflect.ValueOf(value)
	for reflected.IsValid() && (reflected.Kind() == reflect.Interface || reflected.Kind() == reflect.Pointer) {
		if reflected.IsNil() {
			return nil, nil
		}
		reflected = reflected.Elem()
	}
	if !reflected.IsValid() {
		return nil, nil
	}
	switch reflected.Kind() {
	case reflect.Bool:
		return reflected.Bool(), nil
	case reflect.String:
		return canonicalTypedStringV1(reflected.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		integer := reflected.Int()
		if integer < -typedValueMaximumSafeInteger || integer > typedValueMaximumSafeInteger {
			return nil, errors.New("typed integer exceeds the safe JSON range")
		}
		return integer, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		integer := reflected.Uint()
		if integer > uint64(typedValueMaximumSafeInteger) {
			return nil, errors.New("typed integer exceeds the safe JSON range")
		}
		return int64(integer), nil
	case reflect.Float32, reflect.Float64:
		return nil, errors.New("typed floating-point numbers are forbidden")
	case reflect.Slice, reflect.Array:
		if depth > maximumDepth {
			return nil, errors.New("typed value exceeds the nesting depth limit")
		}
		if reflected.Len() > typedValueMaximumMembers {
			return nil, errors.New("typed array exceeds the member limit")
		}
		result := make([]any, reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			item, err := canonicalTypedValueV1WithDepth(reflected.Index(index).Interface(), depth+1, maximumDepth)
			if err != nil {
				return nil, err
			}
			result[index] = item
		}
		return result, nil
	case reflect.Map:
		if depth > maximumDepth {
			return nil, errors.New("typed value exceeds the nesting depth limit")
		}
		if reflected.Type().Key().Kind() != reflect.String {
			return nil, errors.New("typed object keys must be strings")
		}
		if reflected.Len() > typedValueMaximumMembers {
			return nil, errors.New("typed object exceeds the member limit")
		}
		result := make(map[string]any, reflected.Len())
		iterator := reflected.MapRange()
		for iterator.Next() {
			key := iterator.Key().String()
			if !utf8.ValidString(key) || utf8.RuneCountInString(key) == 0 ||
				utf8.RuneCountInString(key) > typedValueMaximumKeyRunes {
				return nil, errors.New("typed object key is invalid")
			}
			if rawFeatureSecretNameV1(key) {
				return nil, ErrSecretDetected
			}
			if _, duplicate := result[key]; duplicate {
				return nil, errors.New("typed object contains a duplicate normalized key")
			}
			item, err := canonicalTypedValueV1WithDepth(iterator.Value().Interface(), depth+1, maximumDepth)
			if err != nil {
				return nil, err
			}
			result[key] = item
		}
		return result, nil
	default:
		return nil, errors.New("typed value contains an unsupported value")
	}
}

func canonicalTypedStringV1(value string) (string, error) {
	if !utf8.ValidString(value) || len(value) > typedValueMaximumStringBytes {
		return "", errors.New("typed string exceeds the UTF-8 byte limit")
	}
	normalized := strings.TrimSpace(norm.NFKC.String(value))
	upper := strings.ToUpper(normalized)
	for _, boundary := range []string{
		"-----BEGIN PRIVATE KEY-----",
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN EC PRIVATE KEY-----",
		"-----BEGIN ENCRYPTED PRIVATE KEY-----",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"-----BEGIN DSA PRIVATE KEY-----",
		"-----BEGIN PGP PRIVATE KEY BLOCK-----",
	} {
		if strings.Contains(upper, boundary) {
			return "", ErrSecretDetected
		}
	}
	fields := strings.Fields(normalized)
	if len(fields) > 1 && strings.EqualFold(fields[0], "bearer") &&
		strings.TrimSpace(strings.TrimPrefix(normalized, fields[0])) != "" {
		return "", ErrSecretDetected
	}
	return value, nil
}

func rawFeatureSecretNameV1(value string) bool {
	normalized := normalizeRawFeatureFieldNameV1(value)
	if _, denied := rawFeatureSecretNames[normalized]; denied {
		return true
	}
	compact := strings.ReplaceAll(normalized, "_", "")
	for denied := range rawFeatureSecretNames {
		if compact == strings.ReplaceAll(denied, "_", "") {
			return true
		}
	}
	return false
}

func normalizeRawFeatureFieldNameV1(value string) string {
	value = norm.NFKC.String(value)
	var result strings.Builder
	underscore := false
	var previous rune
	for _, current := range value {
		if current >= 'A' && current <= 'Z' &&
			(previous >= 'a' && previous <= 'z' || previous >= '0' && previous <= '9') {
			if result.Len() > 0 && !underscore {
				result.WriteByte('_')
			}
			underscore = true
		}
		switch {
		case current >= 'A' && current <= 'Z':
			result.WriteRune(unicode.ToLower(current))
			underscore = false
		case current >= 'a' && current <= 'z' || current >= '0' && current <= '9':
			result.WriteRune(current)
			underscore = false
		default:
			if result.Len() > 0 && !underscore {
				result.WriteByte('_')
			}
			underscore = true
		}
		previous = current
	}
	return strings.Trim(result.String(), "_")
}

func cloneTypedValueV1(value any) any {
	switch typed := value.(type) {
	case nil, bool, int64, string:
		return typed
	case []any:
		result := make([]any, len(typed))
		for index := range typed {
			result[index] = cloneTypedValueV1(typed[index])
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = cloneTypedValueV1(item)
		}
		return result
	default:
		return nil
	}
}

func appendCanonicalJSONV1(output *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		output.WriteString(strconv.FormatBool(typed))
	case int64:
		output.WriteString(strconv.FormatInt(typed, 10))
	case string:
		appendCanonicalJSONStringV1(output, typed)
	case []any:
		output.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := appendCanonicalJSONV1(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(left, right int) bool {
			return lessUTF16V1(keys[left], keys[right])
		})
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := appendCanonicalJSONV1(output, key); err != nil {
				return err
			}
			output.WriteByte(':')
			if err := appendCanonicalJSONV1(output, typed[key]); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return errors.New("typed value contains a non-canonical value")
	}
	return nil
}

func appendCanonicalJSONStringV1(output *bytes.Buffer, value string) {
	const hexadecimal = "0123456789abcdef"

	output.WriteByte('"')
	for _, current := range value {
		switch current {
		case '"', '\\':
			output.WriteByte('\\')
			output.WriteRune(current)
		case '\b':
			output.WriteString(`\b`)
		case '\t':
			output.WriteString(`\t`)
		case '\n':
			output.WriteString(`\n`)
		case '\f':
			output.WriteString(`\f`)
		case '\r':
			output.WriteString(`\r`)
		default:
			if current >= 0 && current <= 0x1f {
				output.WriteString(`\u00`)
				output.WriteByte(hexadecimal[byte(current)>>4])
				output.WriteByte(hexadecimal[byte(current)&0x0f])
				continue
			}
			output.WriteRune(current)
		}
	}
	output.WriteByte('"')
}

func lessUTF16V1(left, right string) bool {
	leftUnits := utf16.Encode([]rune(left))
	rightUnits := utf16.Encode([]rune(right))
	for index := 0; index < len(leftUnits) && index < len(rightUnits); index++ {
		if leftUnits[index] != rightUnits[index] {
			return leftUnits[index] < rightUnits[index]
		}
	}
	return len(leftUnits) < len(rightUnits)
}

func decodeTypedJSONV1(data []byte) (any, error) {
	return decodeTypedJSONV1WithDepth(data, typedValueMaximumDepth)
}

func decodeTypedJSONV1WithDepth(data []byte, maximumDepth int) (any, error) {
	if len(data) > typedValueMaximumJCSBytes {
		return nil, errors.New("typed JSON exceeds the byte limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeTypedJSONTokenV1(decoder, 1, maximumDepth)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("typed JSON contains trailing data")
	}
	return value, nil
}

func decodeTypedJSONTokenV1(decoder *json.Decoder, depth int, maximumDepth int) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '[':
		if depth > maximumDepth {
			return nil, errors.New("typed value exceeds the nesting depth limit")
		}
		result := make([]any, 0)
		for decoder.More() {
			if len(result) >= typedValueMaximumMembers {
				return nil, errors.New("typed array exceeds the member limit")
			}
			item, err := decodeTypedJSONTokenV1(decoder, depth+1, maximumDepth)
			if err != nil {
				return nil, err
			}
			result = append(result, item)
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
			return nil, errors.New("typed array is malformed")
		}
		return result, nil
	case '{':
		if depth > maximumDepth {
			return nil, errors.New("typed value exceeds the nesting depth limit")
		}
		result := make(map[string]any)
		for decoder.More() {
			if len(result) >= typedValueMaximumMembers {
				return nil, errors.New("typed object exceeds the member limit")
			}
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("typed object key is malformed")
			}
			if _, duplicate := result[key]; duplicate {
				return nil, errors.New("typed object contains a duplicate key")
			}
			item, err := decodeTypedJSONTokenV1(decoder, depth+1, maximumDepth)
			if err != nil {
				return nil, err
			}
			result[key] = item
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
			return nil, errors.New("typed object is malformed")
		}
		return result, nil
	default:
		return nil, errors.New("typed JSON contains an invalid delimiter")
	}
}
