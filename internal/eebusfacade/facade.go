// Package eebusfacade contains the internal compile-time boundary to
// enbility/eebus-go.
//
// The package is deliberately internal: public Helianthus packages expose raw
// runtime and evidence contracts only, while this package proves the pinned
// eeBUS dependency can be isolated before any listener, trust store, or gateway
// sidecar exists.
package eebusfacade

import (
	"reflect"
	"slices"
	"sort"
	"strconv"

	eebusapi "github.com/enbility/eebus-go/api"
)

const (
	// EEBusGoModulePath is the only accepted upstream eeBUS runtime module.
	EEBusGoModulePath = "github.com/enbility/eebus-go"
	// EEBusGoVersion is the M3 spike pin.
	EEBusGoVersion = "v0.7.0"
	apiImportPath  = EEBusGoModulePath + "/api"
)

// ModulePin is plain internal evidence about the selected upstream module.
type ModulePin struct {
	Path           string
	Version        string
	ReplacePath    string
	ReplaceVersion string
}

// BoundaryEvidence records the isolation guarantees expected from this spike.
type BoundaryEvidence struct {
	InternalOnly         bool
	PublicTypesHidden    bool
	RuntimeSideEffects   bool
	GatewayImports       bool
	PersistentTrustState bool
}

// APIShapeEvidence records the small eebus-go/api surface used by the spike.
type APIShapeEvidence struct {
	ImportPath                    string
	RequiredServiceReaders        []string
	RequiredReaderCallbacks       []string
	RequiredConfigurationReaders  []string
	ExcludedRuntimeOrMutators     []string
	ServiceReaderShapeBound       bool
	ConfigurationConstructorBound bool
}

// Evidence is the complete internal facade evidence document for MSP-03A.
type Evidence struct {
	Module   ModulePin
	Boundary BoundaryEvidence
	API      APIShapeEvidence
}

// StaticEvidence returns deterministic evidence for review and tests.
func StaticEvidence() Evidence {
	return Evidence{
		Module: ModulePin{
			Path:    EEBusGoModulePath,
			Version: EEBusGoVersion,
		},
		Boundary: BoundaryEvidence{
			InternalOnly:         true,
			PublicTypesHidden:    true,
			RuntimeSideEffects:   false,
			GatewayImports:       false,
			PersistentTrustState: false,
		},
		API: ExpectedControlSurface(),
	}
}

// ExpectedControlSurface returns the eebus-go/api calls this spike binds to.
func ExpectedControlSurface() APIShapeEvidence {
	return APIShapeEvidence{
		ImportPath: apiImportPath,
		RequiredServiceReaders: slices.Clone([]string{
			"Configuration",
			"IsAutoAcceptEnabled",
			"PairingDetailForSki",
			"RemoteServiceForSKI",
		}),
		RequiredReaderCallbacks: serviceReaderCallbackNames(),
		RequiredConfigurationReaders: slices.Clone([]string{
			"DeviceBrand",
			"DeviceModel",
			"DeviceSerialNumber",
			"Identifier",
			"Interfaces",
			"MdnsServiceName",
			"Port",
			"VendorCode",
		}),
		ExcludedRuntimeOrMutators: slices.Clone([]string{
			"AddUseCase",
			"CancelPairingWithSKI",
			"DisconnectSKI",
			"RegisterRemoteSKI",
			"SetAutoAccept",
			"SetLogging",
			"Setup",
			"Shutdown",
			"Start",
			"UnregisterRemoteSKI",
			"UserIsAbleToApproveOrCancelPairingRequests",
		}),
		ServiceReaderShapeBound:       serviceReaderShapeBound,
		ConfigurationConstructorBound: configurationConstructorBound,
	}
}

var (
	serviceReaderShapeBound       = serviceReaderShapeMatches()
	configurationConstructor      = eebusapi.NewConfiguration
	configurationConstructorBound = true
)

func requireServiceReaders(service eebusapi.ServiceInterface) {
	_ = service.Configuration
	_ = service.IsAutoAcceptEnabled
	_ = service.PairingDetailForSki
	_ = service.RemoteServiceForSKI
}

func requireConfigurationReaders(configuration *eebusapi.Configuration) {
	_ = configuration.DeviceBrand
	_ = configuration.DeviceModel
	_ = configuration.DeviceSerialNumber
	_ = configuration.Identifier
	_ = configuration.Interfaces
	_ = configuration.MdnsServiceName
	_ = configuration.Port
	_ = configuration.VendorCode
}

func serviceReaderCallbackNames() []string {
	signatures := serviceReaderCallbackSignatures()
	names := make([]string, 0, len(signatures))
	for _, signature := range signatures {
		names = append(names, signature.Name)
	}
	return names
}

type callbackSignature struct {
	Name   string
	Inputs []string
}

func serviceReaderShapeMatches() bool {
	got := serviceReaderCallbackSignatures()
	want := expectedServiceReaderCallbackSignatures()
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Name != want[i].Name || !slices.Equal(got[i].Inputs, want[i].Inputs) {
			return false
		}
	}
	return true
}

func serviceReaderCallbackSignatures() []callbackSignature {
	interfaceType := reflect.TypeOf((*eebusapi.ServiceReaderInterface)(nil)).Elem()
	signatures := make([]callbackSignature, 0, interfaceType.NumMethod())
	for i := 0; i < interfaceType.NumMethod(); i++ {
		method := interfaceType.Method(i)
		inputs := make([]string, 0, method.Type.NumIn())
		for j := 0; j < method.Type.NumIn(); j++ {
			inputs = append(inputs, typeKey(method.Type.In(j)))
		}
		signatures = append(signatures, callbackSignature{
			Name:   method.Name,
			Inputs: inputs,
		})
	}
	sort.Slice(signatures, func(i, j int) bool {
		return signatures[i].Name < signatures[j].Name
	})
	return signatures
}

func expectedServiceReaderCallbackSignatures() []callbackSignature {
	return []callbackSignature{
		{
			Name: "RemoteSKIConnected",
			Inputs: []string{
				"github.com/enbility/eebus-go/api.ServiceInterface",
				"string",
			},
		},
		{
			Name: "RemoteSKIDisconnected",
			Inputs: []string{
				"github.com/enbility/eebus-go/api.ServiceInterface",
				"string",
			},
		},
		{
			Name: "ServicePairingDetailUpdate",
			Inputs: []string{
				"string",
				"*github.com/enbility/ship-go/api.ConnectionStateDetail",
			},
		},
		{
			Name: "ServiceShipIDUpdate",
			Inputs: []string{
				"string",
				"string",
			},
		},
		{
			Name: "VisibleRemoteServicesUpdated",
			Inputs: []string{
				"github.com/enbility/eebus-go/api.ServiceInterface",
				"[]github.com/enbility/ship-go/api.RemoteService",
			},
		},
	}
}

func typeKey(typ reflect.Type) string {
	switch typ.Kind() {
	case reflect.Pointer:
		return "*" + typeKey(typ.Elem())
	case reflect.Slice:
		return "[]" + typeKey(typ.Elem())
	case reflect.Array:
		return "[" + strconv.Itoa(typ.Len()) + "]" + typeKey(typ.Elem())
	}
	if typ.PkgPath() != "" && typ.Name() != "" {
		return typ.PkgPath() + "." + typ.Name()
	}
	return typ.String()
}
