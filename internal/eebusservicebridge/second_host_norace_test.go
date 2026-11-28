//go:build !eebusreg_second_host && !race

package eebusservicebridge_test

func secondHostRaceEnabled() bool { return false }
