// Package eebusfacade isolates the internal eebus-go compile-time boundary.
package eebusfacade

import (
	"reflect"
	"slices"
	"sort"
	"strconv"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
)

const (
	EEBusGoModulePath = "github.com/Project-Helianthus/helianthus-eebus-go"
	EEBusGoVersion    = "v0.7.1-helianthus.3"
	apiImportPath     = EEBusGoModulePath + "/api"
)

type ModulePin struct {
	Path           string
	Version        string
	ReplacePath    string
	ReplaceVersion string
}

type BoundaryEvidence struct {
	InternalOnly         bool
	PublicTypesHidden    bool
	RuntimeSideEffects   bool
	GatewayImports       bool
	PersistentTrustState bool
}

type APIShapeEvidence struct {
	ImportPath                    string
	RequiredServiceReaders        []string
	RequiredReaderCallbacks       []string
	RequiredConfigurationReaders  []string
	ExcludedRuntimeOrMutators     []string
	ServiceReaderShapeBound       bool
	ConfigurationConstructorBound bool
}

type Evidence struct {
	Module   ModulePin
	Boundary BoundaryEvidence
	API      APIShapeEvidence
}

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
			"SetPairingRegistration",
			"Setup",
			"Shutdown",
			"Start",
			"UnregisterRemoteSKI",
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
				"github.com/Project-Helianthus/helianthus-eebus-go/api.ServiceInterface",
				"string",
			},
		},
		{
			Name: "RemoteSKIDisconnected",
			Inputs: []string{
				"github.com/Project-Helianthus/helianthus-eebus-go/api.ServiceInterface",
				"string",
			},
		},
		{
			Name: "ServicePairingDetailUpdate",
			Inputs: []string{
				"string",
				"*github.com/Project-Helianthus/helianthus-ship-go/api.ConnectionStateDetail",
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
				"github.com/Project-Helianthus/helianthus-eebus-go/api.ServiceInterface",
				"[]github.com/Project-Helianthus/helianthus-ship-go/api.RemoteService",
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
