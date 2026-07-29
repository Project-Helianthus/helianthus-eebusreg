package eebusraw

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestIssue91TypedReadRequestDataAndBindings(t *testing.T) {
	request, data := issue91ValidReadResult(t)

	t.Run("canonical typed request data validates", func(t *testing.T) {
		if terminal := ValidateFeatureDataGetDataV1(request, data, nil); terminal != nil {
			t.Fatalf("valid canonical typed raw_request.data rejected: %+v", terminal)
		}
	})

	tests := map[string]func(*ReadObservationV1){
		"request error number": func(observation *ReadObservationV1) {
			number := uint64(1)
			observation.RawRequest.Data = nil
			observation.RawRequest.ErrorNumber = &number
			issue91RehashObservation(t, observation)
		},
		"correlation mismatch": func(observation *ReadObservationV1) {
			observation.RawResponse.CorrelationKey++
			issue91RehashObservation(t, observation)
		},
		"request function mismatch": func(observation *ReadObservationV1) {
			observation.RawRequest.Function = "measurementDescriptionListData"
			issue91RehashObservation(t, observation)
		},
		"response function mismatch": func(observation *ReadObservationV1) {
			observation.RawResponse.Function = "measurementDescriptionListData"
			issue91RehashObservation(t, observation)
		},
		"response value mismatch": func(observation *ReadObservationV1) {
			value := issue91TypedValue(t, map[string]any{
				"measurementData": []any{},
			})
			observation.RawResponse.Data = &value
			issue91RehashObservation(t, observation)
		},
		"data hash tampering": func(observation *ReadObservationV1) {
			observation.DataHash = HashV1("sha256:" + strings.Repeat("0", 64))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := data.Clone()
			mutate(&candidate.Results[0])
			if terminal := ValidateFeatureDataGetDataV1(request, candidate, nil); terminal == nil {
				t.Fatal("broken raw READ binding was accepted")
			}
		})
	}

	t.Run("malformed typed request data", func(t *testing.T) {
		candidate := data.Clone()
		malformed := TypedValueV1{value: math.NaN()}
		if malformed.Validate() == nil {
			t.Fatal("malformed request fixture unexpectedly validates")
		}
		candidate.Results[0].RawRequest.Data = &malformed
		if terminal := ValidateFeatureDataGetDataV1(request, candidate, nil); terminal == nil {
			t.Fatal("malformed typed raw_request.data was accepted")
		}
	})
}

func issue91ValidReadResult(
	t *testing.T,
) (FeatureDataGetRequestV1, FeatureDataGetDataV1) {
	t.Helper()
	now := time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC)
	target := FeatureTargetV1{
		RemoteSKI:      strings.Repeat("a", 40),
		SHIPID:         "issue91-ship",
		DeviceAddress:  "issue91-device",
		EntityAddress:  []uint64{1},
		FeatureAddress: 2,
		FeatureType:    "Measurement",
		FeatureRole:    FeatureRoleV1Server,
		Function:       "measurementListData",
		Operation:      OperationV1Read,
	}
	requestData := issue91TypedValue(t, map[string]any{
		"measurementListData": map[string]any{},
	})
	responseData := issue91TypedValue(t, map[string]any{
		"measurementData": []any{
			map[string]any{"measurementId": int64(11)},
		},
	})
	observation := ReadObservationV1{
		Target:  target,
		Runtime: RuntimeBindingV1{RuntimeEpoch: 7, ConnectionGeneration: 3},
		RawRequest: ProtocolMessageV1{
			Classifier:     "READ",
			CorrelationKey: 91,
			Function:       target.Function,
			Data:           &requestData,
		},
		RawResponse: ProtocolMessageV1{
			Classifier:     "REPLY",
			CorrelationKey: 91,
			Function:       target.Function,
			Data:           &responseData,
		},
		Value:         responseData,
		RequestedAt:   now,
		ReceivedAt:    now.Add(time.Millisecond),
		DataTimestamp: now.Add(time.Millisecond),
		Source:        ObservationSourceV1Live,
		ReadToken: ReadTokenV1{
			ReadToken:   strings.Repeat("E", 43),
			ExpiresAt:   now.Add(time.Minute),
			BindingHash: HashV1("sha256:" + strings.Repeat("1", 64)),
		},
	}
	issue91RehashObservation(t, &observation)
	return FeatureDataGetRequestV1{
			Targets: []FeatureTargetV1{target},
		}, FeatureDataGetDataV1{
			Results:  []ReadObservationV1{observation},
			Complete: true,
		}
}

func issue91TypedValue(t *testing.T, value any) TypedValueV1 {
	t.Helper()
	typed, err := NewTypedValueV1(value)
	if err != nil {
		t.Fatal(err)
	}
	return typed
}

func issue91RehashObservation(t *testing.T, observation *ReadObservationV1) {
	t.Helper()
	hash, err := observation.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	observation.DataHash = hash
}
