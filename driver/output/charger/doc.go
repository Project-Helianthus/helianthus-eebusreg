// Package charger decides the use cases an EVSE entity announces and the
// readings and server features those use cases and its admitted operations attach.
//
// A charger lens that is not admitted announces nothing. An EV actor
// announces nothing. Monitoring of Power Consumption is announced for a
// reported total, a successful same-cut member sum, another admitted
// measurement of the charger table, or the free-text total name. Phase text
// alone does not fill the total. A total that was not reported is that member
// sum, marked calculated. A withheld sum stays the named total loss. The
// free-text total name does not register the use case and does not hide that
// loss when no total row is served. A later cut drops a measurement it does
// not serve, and a cut that no longer announces power monitoring withdraws
// that table. An unknown fault clears a published failure. A lifetime counter
// that is not an exact number of watt-hours is a named loss. EVSE
// Commissioning and Configuration is announced for the operating state or an
// admitted fault status. A rejected server feature withdraws only the use
// case or the operation that needs it. An admitted allocated-current
// operation is recognized. A consumption limit is not refused.
package charger
