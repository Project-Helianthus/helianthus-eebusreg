// Package inbound publishes paired heating remotes into SemReg.
//
// Rows map native heating observations onto thermal resources, relations,
// kelvin facts and ranked sources. A value with no thermal element stays a
// member of this driver's namespace. Identity evidence is the manufacturer
// and serial the device reports, plus a match claim only where the catalog
// lists the token's scheme.
package inbound
