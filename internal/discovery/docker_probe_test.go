package discovery

import (
	"testing"
)

func TestParseRemoteContainers(t *testing.T) {
	output := []byte(`
{"Names":["/nginx"],"Ports":[{"PublicPort":80,"Type":"tcp"}],"Labels":{"portico.name":"Web"}}
{"Names":["/jellyfin"],"Ports":[{"PublicPort":8096,"Type":"tcp"}],"Labels":{"portico.category":"Media"}}
{"Names":["/disabled-app"],"Ports":[{"PublicPort":9999,"Type":"tcp"}],"Labels":{"portico.enable":"false"}}
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
