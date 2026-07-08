// Package eebusraw defines public raw eeBUS data contracts.
//
// MSP-02A introduces a redaction-safe raw runtime identity contract. The
// package keeps unknown protocol fields opaque and does not expose transport
// facade, listener, trust-store, or pairing mutation surfaces.
package eebusraw
