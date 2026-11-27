// Package certification loads and checks documents of certification
// reference records: values that certificates state about measurement
// accuracy, held apart from anything a device publishes.
package certification

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Contract identity, categories, firmware forms, coverage results and the
// stable failure reasons of a rejected document.
const (
	ContractID = "helianthus.semreg.certification-references/v1"
	Source     = "certification"

	CategoryReference   = "reference"
	CategoryGap         = "gap"
	CategoryUnavailable = "unavailable"

	FormNotStated = "not_stated"
	FormRelease   = "release"
	FormRange     = "range"

	CoverageCovered    = "covered"
	CoverageNotCovered = "not_covered"
	CoverageNotStated  = "not_stated"

	ReasonSchema                     = "schema"
	ReasonSourceNotCertification     = "source_not_certification"
	ReasonUnknownMember              = "unknown_member"
	ReasonMissingMember              = "missing_member"
	ReasonStatedValueNotAllowed      = "stated_value_not_allowed"
	ReasonRefusalMissing             = "refusal_missing"
	ReasonInvalidValue               = "invalid_value"
	ReasonFirmwareInvalid            = "firmware_invalid"
	ReasonFirmwareUpperBoundUnstated = "firmware_upper_bound_unstated"
	ReasonStatedValueInvalid         = "stated_value_invalid"
	ReasonCategoryConflict           = "category_conflict"
)

// Decimal is an exact decimal value: Coefficient times ten to Exponent.
type Decimal struct {
	Coefficient string
	Exponent    int
}

// Firmware records the firmware release or range an entry applies to.
type Firmware struct {
	Form               string
	Release            string
	From               string
	To                 string
	UpperBoundStatedBy string
}

// StatedValue is the accuracy value a certificate states.
type StatedValue struct {
	MeasurementType string
	Measured        *bool
	RatioMaximum    *Decimal
	MeasuredMinimum *Decimal
	MeasuredMaximum *Decimal
	RangeMinimum    *Decimal
	RangeMaximum    *Decimal
}

// Entry is one reference, gap or unavailable record.
type Entry struct {
	Source            string
	Category          string
	Driver            string
	Vendor            string
	Flavor            string
	Model             string
	Firmware          Firmware
	Issuer            string
	CertificateNumber string
	IssueDate         string
	StatedValue       *StatedValue
	URL               string
	RetrievedOn       string
	Refusal           string
}

// Document is a loaded certification references document.
type Document struct {
	Contract string
	Entries  []Entry
}

// Finding is the error returned for a rejected document.
type Finding struct {
	Reason string
	Entry  int
	Member string
}

// Error describes the finding by its stable reason, entry and member.
func (f *Finding) Error() string {
	text := "certification references rejected: " + f.Reason
	if f.Entry >= 0 {
		text += " at entry " + strconv.Itoa(f.Entry)
	}
	if f.Member != "" {
		text += " member " + f.Member
	}
	return text
}

type object struct {
	keys   []string
	values map[string]any
}

var (
	releasePattern     = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)
	datePattern        = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	coefficientPattern = regexp.MustCompile(`^(0|-?[1-9]([0-9]*[1-9])?)$`)
	measurementPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	urlPattern         = regexp.MustCompile(`^https://[^ ]+$`)

	commonMembers = []string{"source", "category", "driver", "vendor", "flavor", "model", "firmware"}
	categoryTails = map[string][]string{
		CategoryReference:   {"issuer", "certificate_number", "issue_date", "stated_value", "url", "retrieved_on"},
		CategoryGap:         {"issuer", "url", "retrieved_on"},
		CategoryUnavailable: {"issuer", "url", "retrieved_on", "refusal"},
	}
	textMembers    = []string{"driver", "vendor", "flavor", "model", "issuer", "certificate_number"}
	dateMembers    = []string{"issue_date", "retrieved_on"}
	decimalMembers = []string{"ratio_maximum", "measured_minimum", "measured_maximum", "range_minimum", "range_maximum"}
	statedMembers  = append([]string{"measurement_type", "measured"}, decimalMembers...)
	firmwareTails  = map[string][]string{
		FormNotStated: nil,
		FormRelease:   {"release"},
		FormRange:     {"from", "to", "upper_bound_stated_by"},
	}
)

func categoryMembers(category string) []string {
	return append(append([]string{}, commonMembers...), categoryTails[category]...)
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil
	}
	if delim == '[' {
		items := []any{}
		for dec.More() {
			item, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		_, err := dec.Token()
		return items, err
	}
	obj := &object{values: map[string]any{}}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key := keyTok.(string)
		if _, repeated := obj.values[key]; repeated {
			return nil, fmt.Errorf("repeated object key %q", key)
		}
		value, err := parseValue(dec)
		if err != nil {
			return nil, err
		}
		obj.keys = append(obj.keys, key)
		obj.values[key] = value
	}
	_, err = dec.Token()
	return obj, err
}

func parseDocument(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the document")
	}
	return value, nil
}

func finding(reason string, entry int, member string) *Finding {
	return &Finding{Reason: reason, Entry: entry, Member: member}
}

func (o *object) has(name string) bool {
	_, ok := o.values[name]
	return ok
}

func isText(value any) bool {
	text, ok := value.(string)
	return ok && text != ""
}

func isDate(value any) bool {
	text, ok := value.(string)
	if !ok || !datePattern.MatchString(text) {
		return false
	}
	if strings.HasPrefix(text, "0000") {
		return false
	}
	_, err := time.Parse("2006-01-02", text)
	return err == nil
}

func isURL(value any) bool {
	text, ok := value.(string)
	return ok && urlPattern.MatchString(text)
}

func isDecimal(value any) bool {
	obj, ok := value.(*object)
	if !ok || len(obj.keys) != 2 || !obj.has("coefficient") || !obj.has("exponent10") {
		return false
	}
	coefficient, ok := obj.values["coefficient"].(string)
	if !ok || !coefficientPattern.MatchString(coefficient) {
		return false
	}
	number, ok := obj.values["exponent10"].(json.Number)
	if !ok {
		return false
	}
	exponent, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil || exponent < -18 || exponent > 18 {
		return false
	}
	return coefficient != "0" || exponent == 0
}

func firstMember(entry *object, names []string, bad func(any) bool) string {
	for _, name := range names {
		if value, ok := entry.values[name]; ok && bad(value) {
			return name
		}
	}
	return ""
}

func checkMembers(entry *object, category string) *Finding {
	allowed := categoryMembers(category)
	if category != CategoryReference && entry.has("stated_value") {
		return finding(ReasonStatedValueNotAllowed, -1, "stated_value")
	}
	var extra []string
	for _, key := range entry.keys {
		if !contains(allowed, key) {
			extra = append(extra, key)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return finding(ReasonUnknownMember, -1, extra[0])
	}
	if category == CategoryUnavailable && !isText(entry.values["refusal"]) {
		return finding(ReasonRefusalMissing, -1, "refusal")
	}
	for _, name := range allowed {
		if !entry.has(name) {
			return finding(ReasonMissingMember, -1, name)
		}
	}
	return nil
}

func contains(list []string, name string) bool {
	for _, item := range list {
		if item == name {
			return true
		}
	}
	return false
}

func checkValues(entry *object) *Finding {
	if name := firstMember(entry, textMembers, func(v any) bool { return !isText(v) }); name != "" {
		return finding(ReasonInvalidValue, -1, name)
	}
	if name := firstMember(entry, dateMembers, func(v any) bool { return !isDate(v) }); name != "" {
		return finding(ReasonInvalidValue, -1, name)
	}
	if name := firstMember(entry, []string{"url"}, func(v any) bool { return !isURL(v) }); name != "" {
		return finding(ReasonInvalidValue, -1, name)
	}
	return nil
}

func compareReleases(left, right string) int {
	a, b := releaseParts(left), releaseParts(right)
	for len(a) < len(b) {
		a = append(a, new(big.Int))
	}
	for len(b) < len(a) {
		b = append(b, new(big.Int))
	}
	for i := range a {
		if c := a[i].Cmp(b[i]); c != 0 {
			return c
		}
	}
	return 0
}

func releaseParts(release string) []*big.Int {
	parts := strings.Split(release, ".")
	out := make([]*big.Int, len(parts))
	for i, part := range parts {
		out[i], _ = new(big.Int).SetString(part, 10)
	}
	return out
}

func checkFirmware(entry *object) *Finding {
	bad := finding(ReasonFirmwareInvalid, -1, "firmware")
	firmware, ok := entry.values["firmware"].(*object)
	if !ok {
		return bad
	}
	form, _ := firmware.values["form"].(string)
	tail, known := firmwareTails[form]
	if !known {
		return bad
	}
	if form == FormRange {
		by, _ := firmware.values["upper_bound_stated_by"].(string)
		if by != "certificate" && by != "vendor_document" {
			return finding(ReasonFirmwareUpperBoundUnstated, -1, "firmware")
		}
	}
	if len(firmware.keys) != 1+len(tail) {
		return bad
	}
	for _, name := range tail {
		if !firmware.has(name) {
			return bad
		}
	}
	for _, name := range []string{"release", "from", "to"} {
		if value, present := firmware.values[name]; present {
			text, isString := value.(string)
			if !isString || !releasePattern.MatchString(text) {
				return bad
			}
		}
	}
	if form == FormRange && compareReleases(firmware.values["from"].(string), firmware.values["to"].(string)) > 0 {
		return bad
	}
	return nil
}

func checkStatedValue(entry *object) *Finding {
	value, present := entry.values["stated_value"]
	if !present {
		return nil
	}
	bad := finding(ReasonStatedValueInvalid, -1, "stated_value")
	stated, ok := value.(*object)
	if !ok {
		return bad
	}
	for _, key := range stated.keys {
		if !contains(statedMembers, key) {
			return bad
		}
	}
	measurement, ok := stated.values["measurement_type"].(string)
	if !ok || !measurementPattern.MatchString(measurement) {
		return bad
	}
	if len(stated.keys) < 2 {
		return bad
	}
	if measured, has := stated.values["measured"]; has {
		if _, isBool := measured.(bool); !isBool {
			return bad
		}
	}
	for _, name := range decimalMembers {
		if stated.has(name) && !isDecimal(stated.values[name]) {
			return bad
		}
	}
	return nil
}

func checkEntry(value any, index int) *Finding {
	entry, ok := value.(*object)
	if !ok {
		return finding(ReasonSchema, index, "")
	}
	if source, has := entry.values["source"]; has && source != Source {
		return withEntry(finding(ReasonSourceNotCertification, -1, "source"), index)
	}
	if !entry.has("category") {
		return withEntry(finding(ReasonMissingMember, -1, "category"), index)
	}
	category, isString := entry.values["category"].(string)
	if _, known := categoryTails[category]; !isString || !known {
		return withEntry(finding(ReasonInvalidValue, -1, "category"), index)
	}
	for _, check := range []func() *Finding{
		func() *Finding { return checkMembers(entry, category) },
		func() *Finding { return checkValues(entry) },
		func() *Finding { return checkFirmware(entry) },
		func() *Finding { return checkStatedValue(entry) },
	} {
		if found := check(); found != nil {
			return withEntry(found, index)
		}
	}
	return nil
}

func withEntry(found *Finding, index int) *Finding {
	found.Entry = index
	return found
}

func checkTopLevel(root *object) *Finding {
	if len(root.keys) != 2 || !root.has("contract") || !root.has("entries") || root.values["contract"] != ContractID {
		return finding(ReasonSchema, -1, "")
	}
	if entries, ok := root.values["entries"].([]any); !ok || len(entries) == 0 {
		return finding(ReasonSchema, -1, "")
	}
	return nil
}

func identityKey(entry *object) string {
	parts := []string{}
	for _, name := range []string{"driver", "vendor", "flavor", "model"} {
		parts = append(parts, strconv.Quote(entry.values[name].(string)))
	}
	firmware := entry.values["firmware"].(*object)
	for _, name := range []string{"form", "release", "from", "to", "upper_bound_stated_by"} {
		text, _ := firmware.values[name].(string)
		parts = append(parts, strconv.Quote(text))
	}
	return strings.Join(parts, "|")
}

func checkCategories(entries []any) *Finding {
	seen := map[string]string{}
	for index, value := range entries {
		entry := value.(*object)
		key := identityKey(entry)
		category := entry.values["category"].(string)
		if previous, ok := seen[key]; ok && previous != category {
			return finding(ReasonCategoryConflict, index, "")
		}
		if _, ok := seen[key]; !ok {
			seen[key] = category
		}
	}
	return nil
}

// Load checks a document given as bytes and returns it, or a *Finding.
func Load(data []byte) (*Document, error) {
	parsed, err := parseDocument(data)
	if err != nil {
		return nil, err
	}
	root, ok := parsed.(*object)
	if !ok {
		return nil, finding(ReasonSchema, -1, "")
	}
	entries, ok := root.values["entries"].([]any)
	if !ok {
		return nil, finding(ReasonSchema, -1, "")
	}
	for index, entry := range entries {
		if found := checkEntry(entry, index); found != nil {
			return nil, found
		}
	}
	if found := checkTopLevel(root); found != nil {
		return nil, found
	}
	if found := checkCategories(entries); found != nil {
		return nil, found
	}
	return build(root), nil
}

// Coverage states whether a firmware release is covered by an entry's
// firmware form; a release at or above a stated release is covered.
func Coverage(firmware Firmware, release string) (string, error) {
	if !releasePattern.MatchString(release) {
		return "", errors.New("release is not dot-separated non-negative integers")
	}
	switch firmware.Form {
	case FormNotStated:
		return CoverageNotStated, nil
	case FormRelease:
		if compareReleases(release, firmware.Release) < 0 {
			return CoverageNotCovered, nil
		}
		return CoverageCovered, nil
	case FormRange:
		if compareReleases(release, firmware.From) < 0 || compareReleases(release, firmware.To) > 0 {
			return CoverageNotCovered, nil
		}
		return CoverageCovered, nil
	}
	return "", errors.New("unknown firmware form")
}

func text(obj *object, name string) string {
	value, _ := obj.values[name].(string)
	return value
}

func decimalOf(obj *object, name string) *Decimal {
	value, ok := obj.values[name].(*object)
	if !ok {
		return nil
	}
	exponent, _ := strconv.Atoi(value.values["exponent10"].(json.Number).String())
	return &Decimal{Coefficient: text(value, "coefficient"), Exponent: exponent}
}

func buildStated(obj *object) *StatedValue {
	stated := &StatedValue{
		MeasurementType: text(obj, "measurement_type"),
		RatioMaximum:    decimalOf(obj, "ratio_maximum"),
		MeasuredMinimum: decimalOf(obj, "measured_minimum"),
		MeasuredMaximum: decimalOf(obj, "measured_maximum"),
		RangeMinimum:    decimalOf(obj, "range_minimum"),
		RangeMaximum:    decimalOf(obj, "range_maximum"),
	}
	if measured, ok := obj.values["measured"].(bool); ok {
		stated.Measured = &measured
	}
	return stated
}

func build(root *object) *Document {
	document := &Document{Contract: text(root, "contract")}
	for _, value := range root.values["entries"].([]any) {
		obj := value.(*object)
		firmware := obj.values["firmware"].(*object)
		entry := Entry{
			Source:            text(obj, "source"),
			Category:          text(obj, "category"),
			Driver:            text(obj, "driver"),
			Vendor:            text(obj, "vendor"),
			Flavor:            text(obj, "flavor"),
			Model:             text(obj, "model"),
			Issuer:            text(obj, "issuer"),
			CertificateNumber: text(obj, "certificate_number"),
			IssueDate:         text(obj, "issue_date"),
			URL:               text(obj, "url"),
			RetrievedOn:       text(obj, "retrieved_on"),
			Refusal:           text(obj, "refusal"),
			Firmware: Firmware{
				Form:               text(firmware, "form"),
				Release:            text(firmware, "release"),
				From:               text(firmware, "from"),
				To:                 text(firmware, "to"),
				UpperBoundStatedBy: text(firmware, "upper_bound_stated_by"),
			},
		}
		if stated, ok := obj.values["stated_value"].(*object); ok {
			entry.StatedValue = buildStated(stated)
		}
		document.Entries = append(document.Entries, entry)
	}
	return document
}
