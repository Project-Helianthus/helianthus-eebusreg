// Package charger decides the use cases an EVSE entity announces and the
// server features those use cases and its admitted operations attach.
//
// A charger lens that is not admitted announces nothing. An EV actor
// announces nothing. Monitoring of Power Consumption needs the total active
// power, and phase values are never summed into it. EVSE Commissioning and
// Configuration needs the operating state, and charger statuses never stand
// in for it. A rejected server feature withdraws only the use case or the
// operation that needs it.
package charger
