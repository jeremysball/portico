package discovery

import (
	"context"
	"encoding/xml"
	"os/exec"
	"strconv"
	"time"
)

// nmapRun is the small subset of nmap's -oX schema this package cares about.
type nmapRun struct {
	Hosts []nmapHost `xml:"host"`
}

type nmapHost struct {
	Ports nmapPorts `xml:"ports"`
}

type nmapPorts struct {
	Port []nmapPort `xml:"port"`
}

type nmapPort struct {
	Service nmapService `xml:"service"`
}

type nmapService struct {
	Name    string `xml:"name,attr"`
	Product string `xml:"product,attr"`
	Version string `xml:"version,attr"`
}

// identifyService shells out to nmap for a single-port service/version
// lookup. This is only ever called against a host:port Portico has already
// confirmed is reachable over HTTP(S) (see identifyPass in discovery.go) —
// it's a targeted banner lookup, not a scan: -Pn skips host discovery (we
// already know it's up) and a single -p port keeps it to one target.
func identifyService(ctx context.Context, addr string, port int) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "nmap",
		"-sV", "-Pn", "--version-intensity", "5",
		"-p", strconv.Itoa(port), "-oX", "-", addr)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return parseNmapService(out)
}

// parseNmapService extracts a human-readable "product version" (or just the
// generic service name if nmap couldn't fingerprint a product/version) from
// raw nmap XML output. Split out from identifyService so it can be unit
// tested against a fixture without needing the nmap binary itself.
func parseNmapService(xmlOut []byte) (string, bool) {
	var run nmapRun
	if err := xml.Unmarshal(xmlOut, &run); err != nil {
		return "", false
	}
	if len(run.Hosts) == 0 || len(run.Hosts[0].Ports.Port) == 0 {
		return "", false
	}
	svc := run.Hosts[0].Ports.Port[0].Service
	switch {
	case svc.Product != "" && svc.Version != "":
		return svc.Product + " " + svc.Version, true
	case svc.Product != "":
		return svc.Product, true
	case svc.Name != "":
		return svc.Name, true
	default:
		return "", false
	}
}
