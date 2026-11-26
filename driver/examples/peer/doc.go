// Package peer shows the eeBUS row patterns that the inbound and output rows of a
// driver copy. The read pattern turns the Measurement data a remote peer notified into
// a SemReg fact, and the output pattern turns a SemReg fact into Measurement server data
// on the local node; the remote LoadControl obligation command and the distinct
// TimeSeries and IncentiveTable commands follow in the same package. Every row, node and
// result belongs to one driver instance created from its configured instance key, so
// two instances side by side share nothing.
package peer
