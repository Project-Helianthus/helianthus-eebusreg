// Package inbound publishes paired remotes into SemReg.
//
// Rows map native heating observations onto thermal resources, relations,
// kelvin facts and ranked sources, and native grid, charger and aggregate
// observations onto their catalog elements. A value with no element stays a
// member of this driver's namespace. Identity evidence is the manufacturer
// and serial the device reports, plus a match claim only where the catalog
// lists the token's scheme. Measurements carry no accuracy value.
package inbound
