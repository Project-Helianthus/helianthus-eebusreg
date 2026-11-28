// Package charger decides the use cases an EVSE entity announces and the
// readings and server features those use cases and its admitted operations attach.
//
// A charger lens that is not admitted announces nothing. An EV actor
// announces nothing. Monitoring of Power Consumption is announced for a
// reported total, a successful same-cut member sum, or another admitted
// measurement of the charger table. Phase text alone does not fill the total.
// A total that was not reported is that member sum, marked calculated. A
// withheld sum stays the named total loss. EVSE Commissioning and
// Configuration is announced for the operating state or an admitted fault
// status. A rejected server feature withdraws only the use case or the
// operation that needs it. An admitted allocated-current operation is
// recognized. A consumption limit is not refused.
package charger
