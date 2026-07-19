package discovery

import "testing"

func TestParseNmapServiceProductAndVersion(t *testing.T) {
	xml := []byte(`<?xml version="1.0"?>
<nmaprun>
  <host>
    <status state="up"/>
    <ports>
      <port protocol="tcp" portid="443">
        <state state="open"/>
        <service name="ssl/http" product="nginx" version="1.24.0" method="probed" conf="10"/>
      </port>
    </ports>
  </host>
</nmaprun>`)

	got, ok := parseNmapService(xml)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if want := "nginx 1.24.0"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseNmapServiceNameOnly(t *testing.T) {
	xml := []byte(`<?xml version="1.0"?>
<nmaprun>
  <host>
    <ports>
      <port protocol="tcp" portid="9090">
        <state state="open"/>
        <service name="http" method="probed" conf="3"/>
      </port>
    </ports>
  </host>
</nmaprun>`)

	got, ok := parseNmapService(xml)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if want := "http"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseNmapServiceNoPorts(t *testing.T) {
	xml := []byte(`<?xml version="1.0"?>
<nmaprun>
  <host>
    <status state="down"/>
    <ports></ports>
  </host>
</nmaprun>`)

	if _, ok := parseNmapService(xml); ok {
		t.Error("expected ok=false for a host with no ports reported")
	}
}

func TestParseNmapServiceNoHosts(t *testing.T) {
	xml := []byte(`<?xml version="1.0"?><nmaprun></nmaprun>`)

	if _, ok := parseNmapService(xml); ok {
		t.Error("expected ok=false for no hosts")
	}
}

func TestParseNmapServiceMalformedXML(t *testing.T) {
	if _, ok := parseNmapService([]byte("not xml")); ok {
		t.Error("expected ok=false for malformed XML")
	}
}
