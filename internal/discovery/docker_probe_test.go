package discovery

import (
	"testing"
)

func TestParseRemoteContainers(t *testing.T) {
	output := []byte(`
{"Names":"nginx","Ports":"0.0.0.0:80->80/tcp, :::80->80/tcp","Labels":"portico.name=Web"}
{"Names":"jellyfin","Ports":"0.0.0.0:8096->8096/tcp","Labels":"portico.category=Media"}
{"Names":"disabled-app","Ports":"0.0.0.0:9999->9999/tcp","Labels":"portico.enable=false"}
`)
	containers, err := parseRemoteContainers(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(containers) != 2 {
		t.Fatalf("expected 2 containers (disabled filtered out), got %d", len(containers))
	}
	if containers[0].Name != "nginx" || len(containers[0].Ports) != 1 || containers[0].Ports[0] != 80 {
		t.Errorf("unexpected nginx container: %+v", containers[0])
	}
	if containers[0].Labels["portico.name"] != "Web" {
		t.Errorf("expected portico.name=Web, got %q", containers[0].Labels["portico.name"])
	}
	if containers[1].Name != "jellyfin" || containers[1].Labels["portico.category"] != "Media" {
		t.Errorf("unexpected jellyfin container: %+v", containers[1])
	}
}

func TestParseRemoteContainersSkipsUnpublishedAndLoopback(t *testing.T) {
	output := []byte(`
{"Names":"internal-only","Ports":"9000/tcp","Labels":""}
{"Names":"loopback-bound","Ports":"127.0.0.1:5432->5432/tcp","Labels":""}
{"Names":"loopback-v6-bound","Ports":"[::1]:5433->5433/tcp","Labels":""}
{"Names":"reachable","Ports":"0.0.0.0:8080->80/tcp","Labels":""}
`)
	containers, err := parseRemoteContainers(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(containers) != 4 {
		t.Fatalf("expected 4 containers, got %d", len(containers))
	}
	for _, c := range containers {
		if c.Name != "reachable" && len(c.Ports) != 0 {
			t.Errorf("expected %s to have no reachable ports, got %v", c.Name, c.Ports)
		}
	}
	if containers[3].Name != "reachable" || len(containers[3].Ports) != 1 || containers[3].Ports[0] != 8080 {
		t.Errorf("unexpected reachable container: %+v", containers[3])
	}
}
