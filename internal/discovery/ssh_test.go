package discovery

import (
	"sort"
	"testing"
)

func TestParseSSOutput(t *testing.T) {
	output := []byte(`
State    Recv-Q   Send-Q     Local Address:Port     Peer Address:Port  Process
LISTEN   0        4096       0.0.0.0:8080           0.0.0.0:*          users:(("nginx",pid=1,fd=6))
LISTEN   0        128       0.0.0.0:3000            0.0.0.0:*          users:(("node",pid=2,fd=7))
LISTEN   0        128       127.0.0.1:5432          0.0.0.0:*          users:(("postgres",pid=3,fd=5))
LISTEN   0        128       0.0.0.0:22              0.0.0.0:*          users:(("sshd",pid=4,fd=3))
LISTEN   0        128          [::]:3000                [::]:*          users:(("node",pid=2,fd=8))
LISTEN   0        128          [::]:22                  [::]:*          users:(("sshd",pid=4,fd=4))
`)
	ports, err := parseSSOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sort.Ints(ports)
	if len(ports) != 2 {
		t.Fatalf("expected 2 ports (8080, 3000), got %d: %v", len(ports), ports)
	}
	if ports[0] != 3000 || ports[1] != 8080 {
		t.Errorf("expected [3000, 8080], got %v", ports)
	}
}

func TestParseSSOutputEmpty(t *testing.T) {
	ports, err := parseSSOutput([]byte(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ports) != 0 {
		t.Errorf("expected 0 ports from empty output, got %d", len(ports))
	}
}

func TestParseSSOutputNoListeners(t *testing.T) {
	output := []byte(`State    Recv-Q   Send-Q     Local Address:Port     Peer Address:Port  Process
LISTEN   0        128       127.0.0.1:22            0.0.0.0:*`)
	ports, err := parseSSOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ports) != 0 {
		t.Errorf("expected 0 ports, got %d", len(ports))
	}
}

func TestMergeSSAndDocker(t *testing.T) {
	ssOut := []byte(`
State    Recv-Q   Send-Q     Local Address:Port     Peer Address:Port  Process
LISTEN   0        4096       0.0.0.0:8080           0.0.0.0:*          users:(("someprocess",pid=1,fd=3))
`)
	dockerOut := []byte(`
{"Names":["/nginx"],"Ports":[{"PublicPort":8080,"Type":"tcp"}],"Labels":{"portico.name":"MyWeb"}}
`)

	dockerProbe := NewDockerProbe(nil)
	containers, err := dockerProbe.ParseRemote(dockerOut)
	if err != nil {
		t.Fatalf("unexpected docker parse error: %v", err)
	}
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(containers))
	}
	if containers[0].Name != "nginx" {
		t.Errorf("expected nginx, got %q", containers[0].Name)
	}

	ssPorts, err := parseSSOutput(ssOut)
	if err != nil {
		t.Fatalf("unexpected ss parse error: %v", err)
	}

	portToTarget := make(map[int]struct {
		Name     string
		HasDocker bool
	})
	for _, port := range ssPorts {
		portToTarget[port] = struct {
			Name     string
			HasDocker bool
		}{Name: "ss-port"}
	}
	for _, c := range containers {
		for _, port := range c.Ports {
			portToTarget[port] = struct {
				Name     string
				HasDocker bool
			}{Name: c.Name, HasDocker: true}
		}
	}

	if len(portToTarget) != 1 {
		t.Fatalf("expected 1 unique port after merge, got %d", len(portToTarget))
	}
	if !portToTarget[8080].HasDocker || portToTarget[8080].Name != "nginx" {
		t.Errorf("docker metadata should win for port 8080, got Name=%q HasDocker=%v",
			portToTarget[8080].Name, portToTarget[8080].HasDocker)
	}
}
