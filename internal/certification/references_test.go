// Tests for the certification references loader.
package certification

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

type vector struct {
	ID       string          `json:"id"`
	Document json.RawMessage `json:"document"`
	Expected struct {
		Accepted bool    `json:"accepted"`
		Reason   string  `json:"reason"`
		Entry    *int    `json:"entry"`
		Member   *string `json:"member"`
	} `json:"expected"`
	Coverage []struct {
		Entry   int    `json:"entry"`
		Release string `json:"release"`
		Result  string `json:"result"`
	} `json:"coverage"`
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	data, err := os.ReadFile("../../driverdata/certification/testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Vectors []vector `json:"vectors"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Vectors) < 30 {
		t.Fatalf("only %d vectors", len(file.Vectors))
	}
	return file.Vectors
}

func TestVectorsMatchTheContract(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.ID, func(t *testing.T) {
			document, err := Load(v.Document)
			if v.Expected.Accepted {
				if err != nil {
					t.Fatalf("rejected: %v", err)
				}
				for _, c := range v.Coverage {
					got, err := Coverage(document.Entries[c.Entry].Firmware, c.Release)
					if err != nil || got != c.Result {
						t.Fatalf("coverage of %s: %q %v, want %q", c.Release, got, err, c.Result)
					}
				}
				return
			}
			var found *Finding
			if !asFinding(err, &found) {
				t.Fatalf("want a finding, got %v", err)
			}
			wantEntry, wantMember := -1, ""
			if v.Expected.Entry != nil {
				wantEntry = *v.Expected.Entry
			}
			if v.Expected.Member != nil {
				wantMember = *v.Expected.Member
			}
			if found.Reason != v.Expected.Reason || found.Entry != wantEntry || found.Member != wantMember {
				t.Fatalf("got %+v, want reason %s entry %d member %q", *found, v.Expected.Reason, wantEntry, wantMember)
			}
		})
	}
}

func asFinding(err error, target **Finding) bool {
	found, ok := err.(*Finding)
	if ok {
		*target = found
	}
	return ok
}

func readData(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../driverdata/certification/eebus-inbound.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEEBusInboundHoldsOneGapAndNoReference(t *testing.T) {
	document, err := Load(readData(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(document.Entries))
	}
	entry := document.Entries[0]
	if entry.Category != CategoryGap || entry.Model != "model_not_named_by_driver" || entry.StatedValue != nil {
		t.Fatalf("unexpected entry %+v", entry)
	}
	if entry.Driver != "eebus-inbound" || entry.Source != Source || entry.Firmware.Form != FormNotStated {
		t.Fatalf("unexpected identity %+v", entry)
	}
	for _, e := range document.Entries {
		if e.Category == CategoryReference {
			t.Fatal("a reference entry exists")
		}
	}
}

func TestSchemaCopyIsTheSharedSchema(t *testing.T) {
	data, err := os.ReadFile("../../driverdata/certification/references.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Contract struct {
				Const string `json:"const"`
			} `json:"contract"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties.Contract.Const != ContractID {
		t.Fatalf("schema contract %q differs from %q", schema.Properties.Contract.Const, ContractID)
	}
}

func TestLoadRefusesMalformedDocuments(t *testing.T) {
	good := string(readData(t))
	cases := map[string]string{
		"repeated key":  strings.Replace(good, `"source": "certification",`, `"source": "certification", "source": "certification",`, 1),
		"trailing data": good + "{}",
		"not json":      "{",
		"array root":    "[]",
		"null":          "null",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load([]byte(text)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func pairDocument(first, second string) string {
	return `{"contract":"` + ContractID + `","entries":[` + first + `,` + second + `]}`
}

func gapEntry(driver, vendor, flavor, model, firmware string) string {
	return fmt.Sprintf(`{"source":"certification","category":"gap","driver":%q,"vendor":%q,"flavor":%q,"model":%q,"firmware":%s,"issuer":"i","url":"https://example.invalid/a","retrieved_on":"2000-01-01"}`,
		driver, vendor, flavor, model, firmware)
}

func unavailableEntry(driver, vendor, flavor, model, firmware string) string {
	return strings.Replace(gapEntry(driver, vendor, flavor, model, firmware), `"category":"gap"`, `"category":"unavailable","refusal":"refused"`, 1)
}

func TestCategoryConflictKeyIsEveryIdentityField(t *testing.T) {
	notStated := `{"form":"not_stated"}`
	release := func(r string) string { return `{"form":"release","release":"` + r + `"}` }
	rng := func(from, to, by string) string {
		return `{"form":"range","from":"` + from + `","to":"` + to + `","upper_bound_stated_by":"` + by + `"}`
	}
	base := [5]string{"d", "v", "f", "m", rng("1", "3", "certificate")}
	variants := map[string][5]string{
		"driver":              {"d2", "v", "f", "m", base[4]},
		"vendor":              {"d", "v2", "f", "m", base[4]},
		"flavor":              {"d", "v", "f2", "m", base[4]},
		"model":               {"d", "v", "f", "m2", base[4]},
		"firmware form":       {"d", "v", "f", "m", release("1")},
		"firmware release":    {"d", "v", "f", "m", release("2")},
		"firmware from":       {"d", "v", "f", "m", rng("2", "3", "certificate")},
		"firmware to":         {"d", "v", "f", "m", rng("1", "4", "certificate")},
		"firmware stated by":  {"d", "v", "f", "m", rng("1", "3", "vendor_document")},
		"firmware not stated": {"d", "v", "f", "m", notStated},
	}
	first := gapEntry(base[0], base[1], base[2], base[3], base[4])
	if _, err := Load([]byte(pairDocument(first, unavailableEntry(base[0], base[1], base[2], base[3], base[4])))); err == nil || err.(*Finding).Reason != ReasonCategoryConflict {
		t.Fatalf("identical identity must conflict, got %v", err)
	}
	for name, v := range variants {
		t.Run(name, func(t *testing.T) {
			if _, err := Load([]byte(pairDocument(first, unavailableEntry(v[0], v[1], v[2], v[3], v[4])))); err != nil {
				t.Fatalf("a different %s must not conflict: %v", name, err)
			}
		})
	}
}

func TestSameCategoryTwiceIsAllowed(t *testing.T) {
	entry := gapEntry("d", "v", "f", "m", `{"form":"not_stated"}`)
	if _, err := Load([]byte(pairDocument(entry, entry))); err != nil {
		t.Fatal(err)
	}
}

func TestLoadedReferenceKeepsEveryStatedMember(t *testing.T) {
	text := `{"contract":"` + ContractID + `","entries":[{"source":"certification","category":"reference","driver":"d","vendor":"v","flavor":"f","model":"m","firmware":{"form":"release","release":"1.4.0"},"issuer":"i","certificate_number":"c","issue_date":"2000-01-15","stated_value":{"measurement_type":"active_power","measured":true,"ratio_maximum":{"coefficient":"1","exponent10":-2},"measured_minimum":{"coefficient":"-5","exponent10":3},"measured_maximum":{"coefficient":"7","exponent10":0},"range_minimum":{"coefficient":"0","exponent10":0},"range_maximum":{"coefficient":"9","exponent10":1}},"url":"https://example.invalid/r","retrieved_on":"2000-02-01"}]}`
	document, err := Load([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	e := document.Entries[0]
	s := e.StatedValue
	if e.CertificateNumber != "c" || e.IssueDate != "2000-01-15" || e.Firmware.Release != "1.4.0" || s == nil || s.Measured == nil || !*s.Measured {
		t.Fatalf("entry %+v", e)
	}
	want := map[string]*Decimal{"ratio": s.RatioMaximum, "min": s.MeasuredMinimum, "max": s.MeasuredMaximum, "rmin": s.RangeMinimum, "rmax": s.RangeMaximum}
	values := map[string]Decimal{"ratio": {"1", -2}, "min": {"-5", 3}, "max": {"7", 0}, "rmin": {"0", 0}, "rmax": {"9", 1}}
	for name, got := range want {
		if got == nil || *got != values[name] {
			t.Fatalf("%s = %v, want %v", name, got, values[name])
		}
	}
}

func TestCoverageRefusesMalformedRelease(t *testing.T) {
	if _, err := Coverage(Firmware{Form: FormRelease, Release: "1"}, "v1"); err == nil {
		t.Fatal("accepted a malformed release")
	}
	if _, err := Coverage(Firmware{Form: "ceiling"}, "1"); err == nil {
		t.Fatal("accepted an unknown form")
	}
	got, err := Coverage(Firmware{Form: FormRelease, Release: "1.0"}, "1.0.0")
	if err != nil || got != CoverageCovered {
		t.Fatalf("a missing component counts as zero: %q %v", got, err)
	}
}

func TestYearZeroDateIsRefused(t *testing.T) {
	if isDate("0000-01-01") {
		t.Fatal("year 0000 must be refused")
	}
	if !isDate("2000-02-29") {
		t.Fatal("a valid date must be accepted")
	}
}

func statementDocument(stated string) string {
	return `{"contract":"` + ContractID + `","entries":[{"source":"certification","category":"reference","driver":"d","vendor":"v","flavor":"f","model":"m","firmware":{"form":"release","release":"1.4.0"},"issuer":"i","certificate_number":"c","issue_date":"2000-01-15","stated_value":` + stated + `,"url":"https://example.invalid/r","retrieved_on":"2000-02-01"}]}`
}

func TestLoadedReferenceKeepsTheStatementUnchanged(t *testing.T) {
	const words = "Class B  per the standard the certificate names, as written."
	document, err := Load([]byte(statementDocument(`{"measurement_type":"active_energy","statement":"` + words + `"}`)))
	if err != nil {
		t.Fatal(err)
	}
	s := document.Entries[0].StatedValue
	if s == nil || s.MeasurementType != "active_energy" || s.Statement != words {
		t.Fatalf("stated value %+v", s)
	}
	if s.Measured != nil || s.RatioMaximum != nil || s.RangeMaximum != nil {
		t.Fatalf("a member appeared that the record does not hold: %+v", s)
	}
}

func TestStatementMustBeNonemptyText(t *testing.T) {
	for _, stated := range []string{
		`{"measurement_type":"active_energy","statement":""}`,
		`{"measurement_type":"active_energy","statement":1}`,
		`{"measurement_type":"active_energy","statement":null}`,
	} {
		_, err := Load([]byte(statementDocument(stated)))
		var f *Finding
		if !asFinding(err, &f) || f.Reason != ReasonStatedValueInvalid {
			t.Fatalf("%s: %v", stated, err)
		}
	}
}
