package inbound

import (
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/roweval"
)

// heatingRow is one native heating field. Kelvin rows carry the contract
// affine. Symbol rows name the native token that becomes the catalog token.
// A native token that is not in the map stays an unknown native symbol.
type heatingRow struct {
	Entity    string
	Field     string
	Subject   string
	Fact      string
	Kelvin    bool
	Affine    *roweval.Affine
	Symbols   map[string]string
	Operation string
}

func celsiusAffine() *roweval.Affine {
	return &roweval.Affine{Num: 1, Den: 1, Add: semreg.Decimal{Coefficient: "27315", Exponent10: -2}}
}

func modeSymbols(tokens ...string) map[string]string {
	out := make(map[string]string, len(tokens))
	for _, token := range tokens {
		out[token] = token
	}
	return out
}

func heatingRows() []heatingRow {
	celsius := celsiusAffine()
	zoneMode := modeSymbols("auto", "comfort", "eco", "off", "on")
	roomMode := modeSymbols("auto", "eco", "off", "on")
	dhwMode := modeSymbols("auto", "eco", "manual", "off", "on", "timed", "vacation")
	overrun := modeSymbols("active", "finished", "inactive", "running")
	overrun["true"] = "active"
	overrun["false"] = "inactive"
	return []heatingRow{
		{Entity: "DHWCircuit", Field: "dhw_temperature", Subject: "self", Fact: "thermal.measurement.dhw_temperature", Kelvin: true, Affine: celsius},
		{Entity: "DHWCircuit", Field: "dhw_temperature_setpoint", Subject: "self", Fact: "thermal.setpoint.dhw_temperature_setpoint", Kelvin: true, Affine: celsius, Operation: "thermal.operation.set_dhw_temperature"},
		{Entity: "DHWCircuit", Field: "dhw_mode", Subject: "self", Fact: "thermal.mode.dhw", Symbols: dhwMode, Operation: "thermal.operation.set_dhw_mode"},
		{Entity: "TemperatureSensor", Field: "outdoor_air_temperature", Subject: "system", Fact: "thermal.measurement.outdoor_air_temperature", Kelvin: true, Affine: celsius},
		{Entity: "", Field: "one_time_dhw", Subject: "system", Fact: "thermal.overrun.one_time_dhw", Symbols: overrun, Operation: "thermal.operation.set_overrun_one_time_dhw"},
		{Entity: "HeatingZone", Field: "air_temperature", Subject: "self", Fact: "thermal.measurement.air_temperature", Kelvin: true, Affine: celsius},
		{Entity: "HeatingZone", Field: "temperature_setpoint", Subject: "self", Fact: "thermal.setpoint.temperature_setpoint", Kelvin: true, Affine: celsius, Operation: "thermal.operation.set_temperature"},
		{Entity: "HeatingZone", Field: "zone_mode", Subject: "self", Fact: "thermal.mode.zone", Symbols: zoneMode, Operation: "thermal.operation.set_zone_mode"},
		{Entity: "HVACRoom", Field: "room_air_temperature", Subject: "self", Fact: "thermal.room.measurement_air_temperature", Kelvin: true, Affine: celsius},
		{Entity: "HVACRoom", Field: "room_temperature_setpoint", Subject: "self", Fact: "thermal.room.setpoint_temperature_setpoint", Kelvin: true, Affine: celsius, Operation: "thermal.room.operation_set_temperature"},
		{Entity: "HVACRoom", Field: "room_mode", Subject: "self", Fact: "thermal.room.mode_zone", Symbols: roomMode, Operation: "thermal.room.operation_set_zone_mode"},
	}
}

func findRow(entity, field string) (heatingRow, bool) {
	for _, row := range heatingRows() {
		if row.Field == field && row.Entity == entity {
			return row, true
		}
	}
	return heatingRow{}, false
}

func namespaceLocal(field string) (string, bool) {
	switch field {
	case "operation_mode_changeable":
		return "mode_changeable", true
	case "setpoint_changeable":
		return "setpoint_changeable", true
	case "overrun_changeable":
		return "overrun_changeable", true
	case "heating_activity":
		return "heating_activity", true
	default:
		return "", false
	}
}
