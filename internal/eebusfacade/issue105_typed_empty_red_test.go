package eebusfacade

import (
	"context"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

const issue105TypedEmptyCode eebusraw.ErrorCodeV1 = "typed_empty"

func TestIssue105RawCommandValueDistinguishesTypedEmptyFromFalseZeroAndLabel(t *testing.T) {
	label := spinemodel.LabelType("Parter")
	falseValue := false
	zeroValue := int64(0)

	for _, test := range []struct {
		name    string
		command spinemodel.CmdType
		empty   bool
	}{
		{
			name: "empty typed object",
			command: spinemodel.CmdType{
				DeviceClassificationUserData: &spinemodel.DeviceClassificationUserDataType{},
			},
			empty: true,
		},
		{
			name: "empty typed list",
			command: spinemodel.CmdType{
				MeasurementListData: &spinemodel.MeasurementListDataType{},
			},
			empty: true,
		},
		{
			name: "Parter label",
			command: spinemodel.CmdType{
				DeviceClassificationUserData: &spinemodel.DeviceClassificationUserDataType{
					UserLabel: &label,
				},
			},
		},
		{
			name: "false boolean",
			command: spinemodel.CmdType{
				DeviceConfigurationKeyValueListData: &spinemodel.DeviceConfigurationKeyValueListDataType{
					DeviceConfigurationKeyValueData: []spinemodel.DeviceConfigurationKeyValueDataType{{
						Value: &spinemodel.DeviceConfigurationKeyValueValueType{Boolean: &falseValue},
					}},
				},
			},
		},
		{
			name: "zero integer",
			command: spinemodel.CmdType{
				DeviceConfigurationKeyValueListData: &spinemodel.DeviceConfigurationKeyValueListDataType{
					DeviceConfigurationKeyValueData: []spinemodel.DeviceConfigurationKeyValueDataType{{
						Value: &spinemodel.DeviceConfigurationKeyValueValueType{Integer: &zeroValue},
					}},
				},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := rawCommandValue(test.command, true)
			if test.empty {
				if err == nil {
					t.Fatalf("rawCommandValue() = %+v, want typed-empty rejection", value)
				}
				return
			}
			if err != nil {
				t.Fatalf("rawCommandValue() rejected known non-empty data: %v", err)
			}
			if rawTypedValueEmpty(value.Value()) {
				t.Fatalf("rawCommandValue() classified known data as empty: %#v", value.Value())
			}
		})
	}
}

func TestIssue105ReadTargetReturnsTypedEmptyWithoutObservation(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	target := issue83TargetFromLocator(fixture.locators[0])
	fixture.sender.roundTrip = func(
		_ context.Context,
		request spineapi.CorrelatedRequest,
	) (spineapi.CorrelatedResponse, error) {
		issue105AssertMeasurementReadFactory(t, request)
		return issue83EmptyMeasurementReply(request, 105), nil
	}

	observation, terminal := fixture.bridge.readTarget(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
		target,
	)
	if observation.ReadToken.ReadToken != "" ||
		observation.ReadToken.BindingHash != "" ||
		observation.DataHash != "" {
		t.Fatalf("typed-empty READ produced observation commitments: %+v", observation)
	}
	issue105AssertTypedEmpty(t, terminal)
}

func TestIssue105KnownEmptyWithUnknownEvidenceRemainsDecodeError(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	target := issue83TargetFromLocator(fixture.locators[0])
	const unknownPath = "/datagram/payload/cmd/0/measurementListData/futureValue"
	fixture.sender.roundTrip = func(
		_ context.Context,
		request spineapi.CorrelatedRequest,
	) (spineapi.CorrelatedResponse, error) {
		response := issue83EmptyMeasurementReply(request, 106)
		response.UnknownFields = []spineapi.CorrelatedUnknownField{
			{Path: unknownPath, Value: spineapi.CorrelatedUnknownValue(`{"future":1}`)},
			{Path: "/datagram/header/future", Value: spineapi.CorrelatedUnknownValue(`"excluded"`)},
		}
		return response, nil
	}

	_, terminal := fixture.bridge.readTarget(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
		target,
	)
	if terminal == nil ||
		terminal.Code != eebusraw.ErrorCodeV1DecodeError ||
		terminal.Retriable ||
		terminal.SourceLayer != eebusraw.SourceLayerV1Decode {
		t.Fatalf("known-empty unknown terminal = %+v, want non-retriable decode_error", terminal)
	}
	if terminal.Details == nil ||
		len(terminal.Details.Unknown) != 1 ||
		terminal.Details.Unknown[0].Path != unknownPath {
		t.Fatalf("known-empty unknown evidence = %+v, want one bounded function-data field", terminal.Details)
	}
}

func TestIssue105BatchAggregationUsesTypedEmpty(t *testing.T) {
	t.Run("mixed valid and typed-empty is partial_result", func(t *testing.T) {
		fixture := newIssue83RawBridgeFixture(t)
		fixture.sender.roundTrip = func(
			_ context.Context,
			request spineapi.CorrelatedRequest,
		) (spineapi.CorrelatedResponse, error) {
			if *request.Destination.Feature == 11 {
				return issue83MeasurementReply(request, 107, 11, 215), nil
			}
			return issue83EmptyMeasurementReply(request, 108), nil
		}
		data, terminal := issue105MeasurementBatch(t, fixture)
		if terminal == nil ||
			terminal.Code != eebusraw.ErrorCodeV1PartialResult ||
			data.Complete ||
			len(data.Results) != 1 ||
			len(data.Failures) != 1 {
			t.Fatalf("mixed batch = data:%+v terminal:%+v", data, terminal)
		}
		if data.Results[0].Target.FeatureAddress != 11 ||
			data.Results[0].ReadToken.ReadToken == "" ||
			data.Failures[0].TargetIndex != 1 ||
			data.Failures[0].Target.FeatureAddress != 12 {
			t.Fatalf("mixed batch ordering/commitments = %+v", data)
		}
		issue105AssertTypedEmpty(t, &data.Failures[0].Error)
	})

	t.Run("all typed-empty returns typed_empty", func(t *testing.T) {
		fixture := newIssue83RawBridgeFixture(t)
		fixture.sender.roundTrip = func(
			_ context.Context,
			request spineapi.CorrelatedRequest,
		) (spineapi.CorrelatedResponse, error) {
			return issue83EmptyMeasurementReply(
				request,
				spinemodel.MsgCounterType(109+*request.Destination.Feature),
			), nil
		}
		data, terminal := issue105MeasurementBatch(t, fixture)
		if data.Complete || len(data.Results) != 0 || len(data.Failures) != 0 {
			t.Fatalf("all-empty batch exposed partial data: %+v", data)
		}
		issue105AssertTypedEmpty(t, terminal)
	})
}

func issue105MeasurementBatch(
	t *testing.T,
	fixture issue83RawBridgeFixture,
) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1) {
	t.Helper()
	return fixture.bridge.featuresDataGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
		eebusraw.FeatureDataGetRequestV1{
			Targets: []eebusraw.FeatureTargetV1{
				issue83TargetFromLocator(fixture.locators[0]),
				issue83TargetFromLocator(fixture.locators[1]),
			},
			TimeoutMS: 1000,
		},
	)
}

func issue105AssertMeasurementReadFactory(t *testing.T, request spineapi.CorrelatedRequest) {
	t.Helper()
	data, err := request.Cmd.Data()
	if err != nil ||
		request.Classifier != spinemodel.CmdClassifierTypeRead ||
		data.Function == nil ||
		*data.Function != spinemodel.FunctionTypeMeasurementListData {
		t.Fatalf("production READ factory command = classifier:%q data:%+v error:%v", request.Classifier, data, err)
	}
}

func issue105AssertTypedEmpty(t *testing.T, terminal *eebusraw.ErrorV1) {
	t.Helper()
	if terminal == nil ||
		terminal.Code != issue105TypedEmptyCode ||
		terminal.Retriable ||
		terminal.SourceLayer != eebusraw.SourceLayerV1Remote {
		t.Fatalf("terminal = %+v, want non-retriable remote-layer typed_empty", terminal)
	}
}
