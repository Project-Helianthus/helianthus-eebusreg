package inbound

// elecRow is one native electrical field. Configuration-only relations are
// not rows: they are gaps. A symbol missing from the closed table is a
// projection loss, not a guessed catalog token.
type elecRow struct {
	entity      string
	field       string
	phase       bool
	target      string
	fact        string
	mode        string
	unit        string
	native      string
	nonnegative bool
	ratio       bool
	symbols     map[string]string
	member      string
}

var communicationSymbols = map[string]string{
	"iec61851":      "iec_61851",
	"iso15118-2ed1": "iso_15118_2",
}

var faultSymbols = map[string]string{
	"failure":         "present",
	"inAlarm":         "present",
	"normalOperation": "clear",
}

var readinessSymbols = map[string]string{
	"normalOperation":     "ready",
	"temporarilyNotReady": "not_ready",
	"off":                 "not_ready",
}

var chargeSymbols = map[string]string{
	"normalOperation": "charging",
	"finished":        "complete",
}

func electricalRows() []elecRow {
	rows := gridElectricalRows()
	rows = append(rows, monitoredElectricalRows()...)
	rows = append(rows, chargerElectricalRows()...)
	rows = append(rows, aggregateElectricalRows()...)
	return rows
}

func findElectricalRow(entityType, field string) (elecRow, bool) {
	for _, row := range electricalRows() {
		if row.entity == entityType && row.field == field {
			return row, true
		}
	}
	if entityType == "" || entityType == "HeatPumpAppliance" {
		return elecRow{}, false
	}
	if _, _, known := electricalKind(entityType); known {
		return elecRow{}, false
	}
	for _, row := range electricalRows() {
		if row.entity == "HeatPumpAppliance" && row.field == field {
			return row, true
		}
	}
	return elecRow{}, false
}

func gridElectricalRows() []elecRow {
	return []elecRow{
		{entity: "GridConnectionPointOfPremises", field: "powerLimitationFactor", target: "self", fact: "infrastructure.limit.pv_curtailment_factor", mode: "percent", unit: "unit.ratio", native: "%", ratio: true},
		{entity: "GridConnectionPointOfPremises", field: "power", target: "self", fact: "infrastructure.power.pcc", mode: "number", unit: "unit.watt", native: "W"},
		{entity: "GridConnectionPointOfPremises", field: "energyFeedIn", target: "meter", fact: "infrastructure.energy.export", mode: "watt_hour", unit: "unit.joule", native: "Wh", nonnegative: true},
		{entity: "GridConnectionPointOfPremises", field: "energyConsumed", target: "meter", fact: "infrastructure.energy.import", mode: "watt_hour", unit: "unit.joule", native: "Wh", nonnegative: true},
		{entity: "GridConnectionPointOfPremises", field: "currentPerPhase", target: "phase", fact: "infrastructure.ac.current", mode: "number", unit: "unit.ampere", native: "A", nonnegative: true, phase: true},
		{entity: "GridConnectionPointOfPremises", field: "voltagePerPhase", target: "phase", fact: "infrastructure.ac.voltage", mode: "number", unit: "unit.volt", native: "V", nonnegative: true, phase: true},
		{entity: "GridConnectionPointOfPremises", field: "frequency", target: "self", fact: "infrastructure.ac.frequency", mode: "number", unit: "unit.hertz", native: "Hz", nonnegative: true},
		{entity: "GridConnectionPointOfPremises", field: "powerPerPhase", target: "phase", fact: "infrastructure.ac.active_power", mode: "number", unit: "unit.watt", native: "W", phase: true},
	}
}

func monitoredElectricalRows() []elecRow {
	pump := "HeatPumpAppliance"
	inverter := "Inverter"
	return []elecRow{
		{entity: pump, field: "power", target: "self", mode: "member", member: "power_total"},
		{entity: pump, field: "frequency", target: "self", mode: "member", member: "frequency"},
		{entity: pump, field: "powerPerPhase", target: "phase", fact: "infrastructure.ac.active_power", mode: "number", unit: "unit.watt", native: "W", phase: true},
		{entity: pump, field: "currentPerPhase", target: "phase", fact: "infrastructure.ac.current", mode: "number", unit: "unit.ampere", native: "A", nonnegative: true, phase: true},
		{entity: pump, field: "voltagePerPhase", target: "phase", fact: "infrastructure.ac.voltage", mode: "number", unit: "unit.volt", native: "V", nonnegative: true, phase: true},
		{entity: pump, field: "energyConsumed", target: "self", fact: "infrastructure.energy.import", mode: "watt_hour", unit: "unit.joule", native: "Wh", nonnegative: true},
		{entity: pump, field: "energyProduced", target: "self", fact: "infrastructure.energy.export", mode: "watt_hour", unit: "unit.joule", native: "Wh", nonnegative: true},
		{entity: inverter, field: "power", target: "self", mode: "member", member: "power_total"},
		{entity: inverter, field: "energyProduced", target: "self", mode: "member", member: "energy_produced"},
		{entity: inverter, field: "energyConsumed", target: "self", mode: "member", member: "energy_consumed"},
		{entity: inverter, field: "frequency", target: "self", fact: "pv.ac.frequency", mode: "number", unit: "unit.hertz", native: "Hz", nonnegative: true},
		{entity: inverter, field: "powerPerPhase", target: "phase", fact: "pv.ac.active_power", mode: "number", unit: "unit.watt", native: "W", phase: true},
		{entity: inverter, field: "currentPerPhase", target: "phase", fact: "pv.ac.current", mode: "number", unit: "unit.ampere", native: "A", nonnegative: true, phase: true},
		{entity: inverter, field: "voltagePerPhase", target: "phase", fact: "pv.ac.voltage", mode: "number", unit: "unit.volt", native: "V", nonnegative: true, phase: true},
	}
}

func chargerElectricalRows() []elecRow {
	evse, ev := "EVSE", "EV"
	return []elecRow{
		{entity: evse, field: "power", target: "self", fact: "evse.ac.total_active_power", mode: "number", unit: "unit.watt", native: "W"},
		{entity: evse, field: "frequency", target: "self", fact: "evse.ac.frequency", mode: "number", unit: "unit.hertz", native: "Hz", nonnegative: true},
		{entity: evse, field: "powerPerPhase", target: "phase", fact: "evse.ac.active_power", mode: "number", unit: "unit.watt", native: "W", phase: true},
		{entity: evse, field: "currentPerPhase", target: "phase", fact: "evse.ac.current", mode: "number", unit: "unit.ampere", native: "A", nonnegative: true, phase: true},
		{entity: evse, field: "voltagePerPhase", target: "phase", fact: "evse.ac.voltage", mode: "number", unit: "unit.volt", native: "V", nonnegative: true, phase: true},
		{entity: evse, field: "energyCharged", target: "session", fact: "evse.energy.session", mode: "watt_hour", unit: "unit.joule", native: "Wh", nonnegative: true},
		{entity: ev, field: "powerPerPhase", target: "phase", fact: "evse.ac.active_power", mode: "number", unit: "unit.watt", native: "W", phase: true},
		{entity: ev, field: "currentPerPhase", target: "phase", fact: "evse.ac.current", mode: "number", unit: "unit.ampere", native: "A", nonnegative: true, phase: true},
		{entity: ev, field: "voltagePerPhase", target: "phase", fact: "evse.ac.voltage", mode: "number", unit: "unit.volt", native: "V", nonnegative: true, phase: true},
		{entity: ev, field: "energyCharged", target: "session", fact: "evse.energy.session", mode: "watt_hour", unit: "unit.joule", native: "Wh", nonnegative: true},
		{entity: ev, field: "stateOfCharge", target: "vehicle", fact: "evse.vehicle.state_of_charge", mode: "percent", unit: "unit.ratio", native: "%", ratio: true},
		{entity: ev, field: "nominalCapacity", target: "vehicle", fact: "evse.vehicle.battery_capacity", mode: "watt_hour", unit: "unit.joule", native: "Wh", nonnegative: true},
		{entity: ev, field: "stateOfHealth", target: "vehicle", fact: "evse.vehicle.state_of_health", mode: "percent", unit: "unit.ratio", native: "%", ratio: true},
		{entity: ev, field: "travelRange", target: "vehicle", fact: "evse.vehicle.travel_range", mode: "number", unit: "unit.metre", native: "m", nonnegative: true},
		{entity: ev, field: "minimumChargingPower", target: "vehicle", fact: "evse.vehicle.minimum_power", mode: "number", unit: "unit.watt", native: "W"},
		{entity: ev, field: "maximumChargingPower", target: "vehicle", fact: "evse.vehicle.maximum_power", mode: "number", unit: "unit.watt", native: "W"},
		{entity: ev, field: "standbyChargingPower", target: "vehicle", fact: "evse.vehicle.standby_power", mode: "number", unit: "unit.watt", native: "W"},
		{entity: ev, field: "identification", target: "vehicle", fact: "evse.vehicle.identifier", mode: "text"},
		{entity: ev, field: "communicationStandard", target: "session", fact: "evse.session.communication_standard", mode: "symbol", symbols: communicationSymbols},
		{entity: ev, field: "asymmetricChargingSupported", target: "session", fact: "evse.session.asymmetric_charging", mode: "boolean"},
	}
}

func aggregateElectricalRows() []elecRow {
	pv, battery := "PVSystem", "BatterySystem"
	return []elecRow{
		{entity: pv, field: "nominalPeakPower", target: "self", fact: "pv.nameplate.peak_power", mode: "number", unit: "unit.watt", native: "W", nonnegative: true},
		{entity: pv, field: "power", target: "self", fact: "pv.vapd.ac_power_total", mode: "number", unit: "unit.watt", native: "W"},
		{entity: pv, field: "yieldTotal", target: "self", fact: "pv.vapd.ac_yield_total", mode: "watt_hour", unit: "unit.joule", native: "Wh"},
		{entity: battery, field: "power", target: "self", fact: "storage.system.ac_power", mode: "number", unit: "unit.watt", native: "W"},
		{entity: battery, field: "energyCharged", target: "self", fact: "storage.system.energy_charged", mode: "watt_hour", unit: "unit.joule", native: "Wh", nonnegative: true},
		{entity: battery, field: "energyDischarged", target: "self", fact: "storage.system.energy_discharged", mode: "watt_hour", unit: "unit.joule", native: "Wh", nonnegative: true},
		{entity: battery, field: "stateOfCharge", target: "self", fact: "storage.system.state_of_charge", mode: "percent", unit: "unit.ratio", native: "%", ratio: true},
	}
}
