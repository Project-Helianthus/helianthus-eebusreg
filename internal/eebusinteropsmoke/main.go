package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	var mode string
	var iface string
	var remoteSKI string
	var timeout time.Duration
	flag.StringVar(&mode, "mode", "live-vr940f", "fake-peer, live-vr940f, or all")
	flag.StringVar(&iface, "interface", "", "network interface name")
	flag.StringVar(&remoteSKI, "remote-ski", "", "optional remote SKI for live pairing smoke")
	flag.DurationVar(&timeout, "timeout", 10*time.Second, "smoke timeout")
	flag.Parse()

	var required []string
	var cases []caseResult
	var notes []string
	switch mode {
	case "fake-peer":
		required = []string{caseFakePeer}
		cases = append(cases, runFakePeerSmoke(fakePeerOptions{Interface: iface, Timeout: timeout}))
	case "live-vr940f":
		required = []string{caseLive}
		cases = append(cases, runLiveVR940fSmoke(context.Background(), liveOptions{Interface: iface, Timeout: timeout, RemoteSKI: remoteSKI}))
	case "all":
		required = []string{caseFakePeer, caseLive}
		cases = append(cases, runFakePeerSmoke(fakePeerOptions{Interface: iface, Timeout: timeout}))
		cases = append(cases, runLiveVR940fSmoke(context.Background(), liveOptions{Interface: iface, Timeout: timeout, RemoteSKI: remoteSKI}))
	default:
		fmt.Fprintf(os.Stderr, "unsupported mode %q\n", mode)
		os.Exit(2)
	}
	if (mode == "live-vr940f" || mode == "all") && remoteSKI == "" {
		notes = append(notes, "live pairing/session/feature graph/reconnect cannot pass without a visible SHIP service and approved remote SKI")
	}
	rep := newReport(mode, required, cases, notes)
	if err := rep.validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	payload, err := rep.jsonBytes()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(payload))
	if rep.Result != resultPass {
		os.Exit(1)
	}
}
