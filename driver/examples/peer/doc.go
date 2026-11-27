// Package peer shows the eeBUS row patterns that the inbound and output rows of a
// driver copy. The read pattern turns the Measurement data a remote peer notified into
// a SemReg fact, and the output pattern turns a SemReg fact into Measurement server data
// on the local node. The remote LoadControl obligation command selects one limit. The
// TimeSeries command selects the writable constraints series in W, and its inverse is a
// series-scoped slot removal. The IncentiveTable command selects the writable simple
// incentive tariff, and its inverse is a whole-table write without that tariff. On the
// pinned receiver, a delete filter whose selector carries both timeSeriesId and
// timeSeriesSlotId removes the whole series, sibling slot included. On the pinned
// receiver, any IncentiveTableDataType write that carries a filter is answered with
// partial updates are not supported for type 'IncentiveTableDataType' and changes
// nothing. Every row, node and result belongs to one driver instance created from its
// configured instance key, so two instances side by side share nothing.
package peer
