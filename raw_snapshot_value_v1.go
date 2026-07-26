package eebusruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	opaqueMaxDepth          = 3
	opaqueMaxMembers        = 32
	opaqueMaxStringBytes    = 4096
	opaqueMaxJCSBytes       = 16384
	opaqueMaxObservations   = 256
	opaqueMaxAggregateBytes = 262144
	opaqueMaxPathLength     = 512
	opaqueMaxSourceLength   = 128
	opaqueMaxKeyLength      = 128
	metadataMaxMembers      = 64
	metadataMaxStringLength = 1024
	maxSafeJSONInteger      = int64(9007199254740991)
)

var snapshotSecretKeysV1 = map[string]struct{}{
	"private_key":            {},
	"private_pem":            {},
	"trust_" + "store_bytes": {},
	"credential_token":       {},
	"bearer_token":           {},
	"session_token":          {},
	"authentication_token":   {},
	"cryptographic_secret":   {},
}

type OpaqueObservationV1 struct {
	Path   string        `json:"path"`
	Source string        `json:"source"`
	Value  OpaqueValueV1 `json:"value"`
}

type OpaqueValueV1 struct {
	Scalar *OpaqueScalarV1           `json:"scalar,omitempty"`
	Array  *[]OpaqueValueV1          `json:"array,omitempty"`
	Object *map[string]OpaqueValueV1 `json:"object,omitempty"`
}

type OpaqueScalarV1 struct {
	Null    *bool   `json:"null,omitempty"`
	Boolean *bool   `json:"boolean,omitempty"`
	Integer *int64  `json:"integer,omitempty"`
	String  *string `json:"string,omitempty"`
}

type MetadataV1 struct {
	Values map[string]MetadataValueV1 `json:"values"`
}

type MetadataValueV1 struct {
	Null    *bool   `json:"null,omitempty"`
	Boolean *bool   `json:"boolean,omitempty"`
	Integer *int64  `json:"integer,omitempty"`
	String  *string `json:"string,omitempty"`
}

func (value OpaqueValueV1) MarshalJSON() ([]byte, error) {
	if err := validateOpaqueValueV1(value, 1); err != nil {
		return nil, err
	}
	switch {
	case value.Scalar != nil:
		return value.Scalar.MarshalJSON()
	case value.Array != nil:
		values := *value.Array
		if values == nil {
			values = []OpaqueValueV1{}
		}
		return marshalJCSV1(values)
	default:
		values := *value.Object
		if values == nil {
			values = map[string]OpaqueValueV1{}
		}
		return marshalJCSV1(values)
	}
}

func (value *OpaqueValueV1) UnmarshalJSON(data []byte) error {
	decoded, err := decodeJSONValueV1(data)
	if err != nil {
		return err
	}
	result, err := opaqueValueFromJSONV1(decoded)
	if err != nil {
		return err
	}
	if err := validateOpaqueValueV1(result, 1); err != nil {
		return err
	}
	*value = result
	return nil
}

func (scalar OpaqueScalarV1) MarshalJSON() ([]byte, error) {
	if err := validateOpaqueScalarV1(scalar); err != nil {
		return nil, err
	}
	switch {
	case scalar.Null != nil:
		return []byte("null"), nil
	case scalar.Boolean != nil:
		return []byte(strconv.FormatBool(*scalar.Boolean)), nil
	case scalar.Integer != nil:
		return []byte(strconv.FormatInt(*scalar.Integer, 10)), nil
	default:
		return appendJSONStringV1(nil, *scalar.String), nil
	}
}

func (scalar *OpaqueScalarV1) UnmarshalJSON(data []byte) error {
	decoded, err := decodeJSONValueV1(data)
	if err != nil {
		return err
	}
	result, err := opaqueScalarFromJSONV1(decoded)
	if err != nil {
		return err
	}
	*scalar = result
	return nil
}

func (metadata MetadataV1) MarshalJSON() ([]byte, error) {
	if err := validateMetadataV1(&metadata); err != nil {
		return nil, err
	}
	values := metadata.Values
	if values == nil {
		values = map[string]MetadataValueV1{}
	}
	return marshalJCSV1(values)
}

func (metadata *MetadataV1) UnmarshalJSON(data []byte) error {
	decoded, err := decodeJSONValueV1(data)
	if err != nil {
		return err
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return errors.New("metadata must be a JSON object")
	}
	result := MetadataV1{Values: make(map[string]MetadataValueV1, len(object))}
	for key, value := range object {
		scalar, err := metadataValueFromJSONV1(value)
		if err != nil {
			return errors.New("metadata contains an unsupported value")
		}
		result.Values[key] = scalar
	}
	if err := validateMetadataV1(&result); err != nil {
		return err
	}
	*metadata = result
	return nil
}

func (value MetadataValueV1) MarshalJSON() ([]byte, error) {
	if err := validateMetadataValueV1(value); err != nil {
		return nil, err
	}
	switch {
	case value.Null != nil:
		return []byte("null"), nil
	case value.Boolean != nil:
		return []byte(strconv.FormatBool(*value.Boolean)), nil
	case value.Integer != nil:
		return []byte(strconv.FormatInt(*value.Integer, 10)), nil
	default:
		return appendJSONStringV1(nil, *value.String), nil
	}
}

func (value *MetadataValueV1) UnmarshalJSON(data []byte) error {
	decoded, err := decodeJSONValueV1(data)
	if err != nil {
		return err
	}
	result, err := metadataValueFromJSONV1(decoded)
	if err != nil {
		return err
	}
	*value = result
	return nil
}

func decodeJSONValueV1(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple JSON values are not allowed")
	}
	return value, nil
}

func opaqueValueFromJSONV1(value any) (OpaqueValueV1, error) {
	switch typed := value.(type) {
	case nil, bool, string, json.Number:
		scalar, err := opaqueScalarFromJSONV1(typed)
		if err != nil {
			return OpaqueValueV1{}, err
		}
		return OpaqueValueV1{Scalar: &scalar}, nil
	case []any:
		values := make([]OpaqueValueV1, len(typed))
		for index, item := range typed {
			converted, err := opaqueValueFromJSONV1(item)
			if err != nil {
				return OpaqueValueV1{}, err
			}
			values[index] = converted
		}
		return OpaqueValueV1{Array: &values}, nil
	case map[string]any:
		values := make(map[string]OpaqueValueV1, len(typed))
		for key, item := range typed {
			converted, err := opaqueValueFromJSONV1(item)
			if err != nil {
				return OpaqueValueV1{}, err
			}
			values[key] = converted
		}
		return OpaqueValueV1{Object: &values}, nil
	default:
		return OpaqueValueV1{}, errors.New("opaque value contains an unsupported JSON value")
	}
}

func opaqueScalarFromJSONV1(value any) (OpaqueScalarV1, error) {
	switch typed := value.(type) {
	case nil:
		present := true
		return OpaqueScalarV1{Null: &present}, nil
	case bool:
		return OpaqueScalarV1{Boolean: &typed}, nil
	case string:
		return OpaqueScalarV1{String: &typed}, nil
	case json.Number:
		if string(typed) == "-0" {
			return OpaqueScalarV1{}, errors.New("opaque numbers must not use negative zero")
		}
		integer, err := strconv.ParseInt(string(typed), 10, 64)
		if err != nil {
			return OpaqueScalarV1{}, errors.New("opaque numbers must be integers")
		}
		return OpaqueScalarV1{Integer: &integer}, nil
	default:
		return OpaqueScalarV1{}, errors.New("opaque scalar contains an unsupported JSON value")
	}
}

func metadataValueFromJSONV1(value any) (MetadataValueV1, error) {
	scalar, err := opaqueScalarFromJSONV1(value)
	if err != nil {
		return MetadataValueV1{}, err
	}
	return MetadataValueV1{
		Null: scalar.Null, Boolean: scalar.Boolean, Integer: scalar.Integer, String: scalar.String,
	}, nil
}

func validateOpaqueObservationSetV1(values []OpaqueObservationV1) (int, int, error) {
	if len(values) > opaqueMaxObservations {
		return 0, 0, errors.New("opaque observation count exceeds the contract limit")
	}
	seen := make(map[string]struct{}, len(values))
	aggregate := 0
	for _, observation := range values {
		if observation.Path == "" || !utf8.ValidString(observation.Path) ||
			utf8.RuneCountInString(observation.Path) > opaqueMaxPathLength {
			return 0, 0, errors.New("opaque observation path is invalid")
		}
		if observation.Source == "" || !utf8.ValidString(observation.Source) ||
			utf8.RuneCountInString(observation.Source) > opaqueMaxSourceLength {
			return 0, 0, errors.New("opaque observation source is invalid")
		}
		if err := validateOpaqueValueV1(observation.Value, 1); err != nil {
			return 0, 0, err
		}
		encoded, err := marshalJCSV1(observation.Value)
		if err != nil {
			return 0, 0, err
		}
		if len(encoded) > opaqueMaxJCSBytes {
			return 0, 0, errors.New("opaque value exceeds the canonical byte limit")
		}
		aggregate += len(encoded)
		key := observation.Path + "\x00" + observation.Source + "\x00" + string(encoded)
		if _, exists := seen[key]; exists {
			return 0, 0, errors.New("opaque observations contain a duplicate")
		}
		seen[key] = struct{}{}
	}
	if aggregate > opaqueMaxAggregateBytes {
		return 0, 0, errors.New("opaque values exceed the aggregate canonical byte limit")
	}
	return len(values), aggregate, nil
}

func validateOpaqueValueV1(value OpaqueValueV1, depth int) error {
	choices := 0
	if value.Scalar != nil {
		choices++
	}
	if value.Array != nil {
		choices++
	}
	if value.Object != nil {
		choices++
	}
	if choices != 1 {
		return errors.New("opaque value must select exactly one JSON value kind")
	}
	if value.Scalar != nil {
		return validateOpaqueScalarV1(*value.Scalar)
	}
	if depth > opaqueMaxDepth {
		return errors.New("opaque value exceeds the nesting depth limit")
	}
	if value.Array != nil {
		if len(*value.Array) > opaqueMaxMembers {
			return errors.New("opaque array exceeds the member limit")
		}
		for _, item := range *value.Array {
			next := depth
			if item.Array != nil || item.Object != nil {
				next++
			}
			if err := validateOpaqueValueV1(item, next); err != nil {
				return err
			}
		}
		return nil
	}
	if len(*value.Object) > opaqueMaxMembers {
		return errors.New("opaque object exceeds the member limit")
	}
	for key, item := range *value.Object {
		if !utf8.ValidString(key) || utf8.RuneCountInString(key) > opaqueMaxKeyLength {
			return errors.New("opaque object key is invalid")
		}
		if _, forbidden := snapshotSecretKeysV1[strings.ToLower(key)]; forbidden {
			return errors.New("snapshot contains forbidden secret material")
		}
		next := depth
		if item.Array != nil || item.Object != nil {
			next++
		}
		if err := validateOpaqueValueV1(item, next); err != nil {
			return err
		}
	}
	return nil
}

func validateOpaqueScalarV1(value OpaqueScalarV1) error {
	choices := 0
	if value.Null != nil {
		choices++
	}
	if value.Boolean != nil {
		choices++
	}
	if value.Integer != nil {
		choices++
	}
	if value.String != nil {
		choices++
	}
	if choices != 1 {
		return errors.New("opaque scalar must select exactly one JSON scalar")
	}
	if value.Null != nil && !*value.Null {
		return errors.New("opaque null marker must be true")
	}
	if value.Integer != nil && (*value.Integer < -maxSafeJSONInteger || *value.Integer > maxSafeJSONInteger) {
		return errors.New("opaque integer exceeds the safe JSON integer range")
	}
	if value.String != nil {
		if !utf8.ValidString(*value.String) || len([]byte(*value.String)) > opaqueMaxStringBytes {
			return errors.New("opaque string exceeds the UTF-8 byte limit")
		}
		if containsPEMMaterialV1(*value.String) {
			return errors.New("snapshot contains forbidden secret material")
		}
	}
	return nil
}

func validateMetadataV1(metadata *MetadataV1) error {
	if metadata == nil {
		return nil
	}
	if len(metadata.Values) > metadataMaxMembers {
		return errors.New("metadata exceeds the member limit")
	}
	for key, value := range metadata.Values {
		if key == "" || !utf8.ValidString(key) || utf8.RuneCountInString(key) > opaqueMaxKeyLength {
			return errors.New("metadata key is invalid")
		}
		if _, forbidden := snapshotSecretKeysV1[strings.ToLower(key)]; forbidden {
			return errors.New("snapshot contains forbidden secret material")
		}
		if err := validateMetadataValueV1(value); err != nil {
			return err
		}
	}
	return nil
}

func validateMetadataValueV1(value MetadataValueV1) error {
	scalar := OpaqueScalarV1{
		Null: value.Null, Boolean: value.Boolean, Integer: value.Integer, String: value.String,
	}
	if err := validateOpaqueScalarV1(scalar); err != nil {
		return err
	}
	if value.String != nil && utf8.RuneCountInString(*value.String) > metadataMaxStringLength {
		return errors.New("metadata string exceeds the length limit")
	}
	return nil
}

func containsPEMMaterialV1(value string) bool {
	normalized := strings.ToUpper(value)
	return strings.Contains(normalized, "-----BEGIN PRIVATE KEY-----") ||
		strings.Contains(normalized, "-----BEGIN RSA PRIVATE KEY-----") ||
		strings.Contains(normalized, "-----BEGIN EC PRIVATE KEY-----")
}

func cloneOpaqueObservationsV1(source []OpaqueObservationV1) []OpaqueObservationV1 {
	if source == nil {
		return nil
	}
	result := make([]OpaqueObservationV1, len(source))
	for index, observation := range source {
		result[index] = observation
		result[index].Value = cloneOpaqueValueV1(observation.Value)
	}
	return result
}

func cloneOpaqueValueV1(source OpaqueValueV1) OpaqueValueV1 {
	result := source
	if source.Scalar != nil {
		scalar := cloneOpaqueScalarV1(*source.Scalar)
		result.Scalar = &scalar
	}
	if source.Array != nil {
		values := make([]OpaqueValueV1, len(*source.Array))
		for index, value := range *source.Array {
			values[index] = cloneOpaqueValueV1(value)
		}
		result.Array = &values
	}
	if source.Object != nil {
		values := make(map[string]OpaqueValueV1, len(*source.Object))
		for key, value := range *source.Object {
			values[key] = cloneOpaqueValueV1(value)
		}
		result.Object = &values
	}
	return result
}

func cloneOpaqueScalarV1(source OpaqueScalarV1) OpaqueScalarV1 {
	result := source
	if source.Null != nil {
		value := *source.Null
		result.Null = &value
	}
	if source.Boolean != nil {
		value := *source.Boolean
		result.Boolean = &value
	}
	if source.Integer != nil {
		value := *source.Integer
		result.Integer = &value
	}
	if source.String != nil {
		value := *source.String
		result.String = &value
	}
	return result
}

func cloneMetadataV1(source *MetadataV1) *MetadataV1 {
	if source == nil {
		return nil
	}
	result := &MetadataV1{Values: make(map[string]MetadataValueV1, len(source.Values))}
	for key, value := range source.Values {
		scalar := cloneOpaqueScalarV1(OpaqueScalarV1{
			Null: value.Null, Boolean: value.Boolean, Integer: value.Integer, String: value.String,
		})
		result.Values[key] = MetadataValueV1{
			Null: scalar.Null, Boolean: scalar.Boolean, Integer: scalar.Integer, String: scalar.String,
		}
	}
	return result
}

func canonicalOpaqueObservationsV1(source []OpaqueObservationV1) []OpaqueObservationV1 {
	result := cloneOpaqueObservationsV1(source)
	sort.Slice(result, func(left, right int) bool {
		if result[left].Path != result[right].Path {
			return result[left].Path < result[right].Path
		}
		if result[left].Source != result[right].Source {
			return result[left].Source < result[right].Source
		}
		leftValue, _ := marshalJCSV1(result[left].Value)
		rightValue, _ := marshalJCSV1(result[right].Value)
		return bytes.Compare(leftValue, rightValue) < 0
	})
	return result
}

func marshalJCSV1(value any) ([]byte, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeJSONValueV1(plain)
	if err != nil {
		return nil, err
	}
	var output []byte
	output, err = appendJCSV1(output, decoded)
	if err != nil {
		return nil, err
	}
	return output, nil
}

func appendJCSV1(output []byte, value any) ([]byte, error) {
	switch typed := value.(type) {
	case nil:
		return append(output, "null"...), nil
	case bool:
		return strconv.AppendBool(output, typed), nil
	case string:
		if !utf8.ValidString(typed) {
			return nil, errors.New("JCS string is not valid UTF-8")
		}
		return appendJSONStringV1(output, typed), nil
	case json.Number:
		integer, err := strconv.ParseInt(string(typed), 10, 64)
		if err != nil {
			return nil, errors.New("JCS numbers must be integers")
		}
		return strconv.AppendInt(output, integer, 10), nil
	case []any:
		output = append(output, '[')
		for index, item := range typed {
			if index != 0 {
				output = append(output, ',')
			}
			var err error
			output, err = appendJCSV1(output, item)
			if err != nil {
				return nil, err
			}
		}
		return append(output, ']'), nil
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(left, right int) bool {
			return utf16LessV1(keys[left], keys[right])
		})
		output = append(output, '{')
		for index, key := range keys {
			if index != 0 {
				output = append(output, ',')
			}
			output = appendJSONStringV1(output, key)
			output = append(output, ':')
			var err error
			output, err = appendJCSV1(output, typed[key])
			if err != nil {
				return nil, err
			}
		}
		return append(output, '}'), nil
	default:
		return nil, fmt.Errorf("unsupported JCS value %T", value)
	}
}

func appendJSONStringV1(output []byte, value string) []byte {
	output = append(output, '"')
	for _, char := range value {
		switch char {
		case '"', '\\':
			output = append(output, '\\', byte(char))
		case '\b':
			output = append(output, '\\', 'b')
		case '\t':
			output = append(output, '\\', 't')
		case '\n':
			output = append(output, '\\', 'n')
		case '\f':
			output = append(output, '\\', 'f')
		case '\r':
			output = append(output, '\\', 'r')
		default:
			if char < 0x20 {
				output = append(output, '\\', 'u', '0', '0')
				output = strconv.AppendInt(output, int64(char>>4), 16)
				output = strconv.AppendInt(output, int64(char&0xf), 16)
			} else {
				output = utf8.AppendRune(output, char)
			}
		}
	}
	return append(output, '"')
}

func utf16LessV1(left, right string) bool {
	leftUnits := utf16.Encode([]rune(left))
	rightUnits := utf16.Encode([]rune(right))
	for index := 0; index < len(leftUnits) && index < len(rightUnits); index++ {
		if leftUnits[index] != rightUnits[index] {
			return leftUnits[index] < rightUnits[index]
		}
	}
	return len(leftUnits) < len(rightUnits)
}
