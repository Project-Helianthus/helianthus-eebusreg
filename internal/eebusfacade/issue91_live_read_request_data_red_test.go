package eebusfacade

import (
	"context"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue91ExecutorGeneratedReadRequestDataSurvivesValidation(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	target := issue83TargetFromLocator(fixture.locators[0])
	request := eebusraw.FeatureDataGetRequestV1{
		Targets:   []eebusraw.FeatureTargetV1{target},
		TimeoutMS: 1000,
	}

	data, terminal := fixture.bridge.featuresDataGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
		request,
	)
	if terminal != nil {
		t.Fatalf("executor-backed features.data.get failed: %+v", terminal)
	}
	if len(data.Results) != 1 {
		t.Fatalf("executor-backed features.data.get returned %d results, want 1", len(data.Results))
	}
	if data.Results[0].RawRequest.Data == nil {
		t.Fatal("executor-generated raw_request.data was stripped")
	}
	if err := data.Results[0].RawRequest.Data.Validate(); err != nil {
		t.Fatalf("executor-generated raw_request.data is not canonical: %v", err)
	}
	if validation := eebusraw.ValidateFeatureDataGetDataV1(request, data, nil); validation != nil {
		t.Fatalf("executor-generated raw_request.data rejected: %+v", validation)
	}
}
