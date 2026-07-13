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
	var remoteSKIFile string
	var operatorProofFile string
	var pairingWindow bool
	var port int
	var repoBranch string
	var repoCommit string
	var timeout time.Duration
	flag.StringVar(&mode, "mode", "live-vr940f", "fake-peer, live-vr940f, or all")
	flag.StringVar(&iface, "interface", "", "network interface name")
	flag.StringVar(&remoteSKIFile, "remote-ski-file", "", "0600 file containing the expected remote SKI")
	flag.StringVar(&operatorProofFile, "operator-proof-file", "", "0600 JSON file containing live operator confirmations")
	flag.BoolVar(&pairingWindow, "pairing-window", false, "advertise a temporary disposable pairing window")
	flag.IntVar(&port, "port", 4712, "local SHIP listener port")
	flag.StringVar(&repoBranch, "repo-branch", "", "source branch embedded in live evidence")
	flag.StringVar(&repoCommit, "repo-commit", "", "source commit embedded in live evidence")
	flag.DurationVar(&timeout, "timeout", 10*time.Minute, "smoke timeout")
	flag.Parse()

	var required []string
	var cases []caseResult
	var notes []string
	var liveEvidence *liveGateEvidence
	remoteSKI, _ := readSecureTextFile(remoteSKIFile)
	liveOpts := liveOptions{
		Interface:        iface,
		Timeout:          timeout,
		Port:             port,
		RemoteSKI:        remoteSKI,
		PairingWindow:    pairingWindow,
		OperatorProofRef: operatorProofFile,
		RepoBranch:       repoBranch,
		RepoCommit:       repoCommit,
	}
	switch mode {
	case "fake-peer":
		required = []string{caseFakePeer}
		cases = append(cases, runFakePeerSmoke(fakePeerOptions{Interface: iface, Timeout: timeout}))
	case "live-vr940f":
		required = []string{caseLive, caseDirectAccess}
		live := runLiveVR940fProof(context.Background(), liveOpts)
		cases = append(cases, live.Cases...)
		liveEvidence = live.LiveEvidence
	case "all":
		required = []string{caseFakePeer, caseLive, caseDirectAccess}
		cases = append(cases, runFakePeerSmoke(fakePeerOptions{Interface: iface, Timeout: timeout}))
		live := runLiveVR940fProof(context.Background(), liveOpts)
		cases = append(cases, live.Cases...)
		liveEvidence = live.LiveEvidence
	default:
		fmt.Fprintf(os.Stderr, "unsupported mode %q\n", mode)
		os.Exit(2)
	}
	if (mode == "live-vr940f" || mode == "all") && remoteSKI == "" {
		notes = append(notes, "live proof requires an expected remote identity supplied through a protected file")
	}
	rep := newReport(mode, required, cases, notes)
	rep.LiveEvidence = liveEvidence
	if repoBranch != "" {
		rep.RepoBranch = repoBranch
	}
	if repoCommit != "" {
		rep.RepoCommit = repoCommit
	}
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
