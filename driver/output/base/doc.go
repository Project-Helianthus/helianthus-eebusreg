// Package base reads SemReg for one eeBUS output instance and applies each
// committed cut to that instance's output binding.
//
// The read is the session's captured cut. Hierarchy and composition supply
// topology. An EVSE also reads the energy lens and the same-cut member sum.
// The feed starts and stops with the instance. It adds no health type and no
// configuration key.
package base
