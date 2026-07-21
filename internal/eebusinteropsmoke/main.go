package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	var mode string
	var iface string
	var repoBranch string
	var repoCommit string
	var timeout time.Duration
	flag.StringVar(&mode, "mode", "fake-peer", "fake-peer")
	flag.StringVar(&iface, "interface", "", "network interface name")
	flag.StringVar(&repoBranch, "repo-branch", "", "source branch embedded in the report")
	flag.StringVar(&repoCommit, "repo-commit", "", "source commit embedded in the report")
	flag.DurationVar(&timeout, "timeout", 10*time.Minute, "smoke timeout")
	flag.Parse()

	var required []string
	var cases []caseResult
	var notes []string
	switch mode {
	case "fake-peer":
		required = []string{caseFakePeer}
		cases = append(cases, runFakePeerSmoke(fakePeerOptions{Interface: iface, Timeout: timeout}))
	default:
		fmt.Fprintf(os.Stderr, "unsupported mode %q\n", mode)
		os.Exit(2)
	}
	rep := newReport(mode, required, cases, notes)
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
