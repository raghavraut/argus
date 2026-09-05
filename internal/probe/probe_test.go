package probe

import (
	"testing"

	"github.com/projectdiscovery/httpx/runner"
)

// Regression: single-doc/IPv6 responses can carry nil TLSData (v0.2 live-fire panic).
func TestMapResultNilTLS(t *testing.T) {
	r := runner.Result{Input: "x.t", StatusCode: 200, Title: "ok", BodyPreview: "ok"}
	hr := mapResult(r)
	if hr.Asset != "x.t" || hr.StatusCode != 200 {
		t.Fatalf("bad mapping: %+v", hr)
	}
	if len(hr.CertSANs) != 0 {
		t.Fatalf("expected no SANs, got %v", hr.CertSANs)
	}
}

func TestMapResultTech(t *testing.T) {
	r := runner.Result{
		Input: "j.t", StatusCode: 200, Title: "Jenkins",
		Technologies: []string{"Jenkins", "Groovy"},
		ResponseHeaders: map[string]interface{}{"Server": "Jetty"},
	}
	hr := mapResult(r)
	if len(hr.Tech) != 2 || hr.Tech[0] != "Jenkins" {
		t.Fatalf("tech lost: %+v", hr.Tech)
	}
	if hr.Headers["server"] != "Jetty" {
		t.Fatalf("headers lost: %+v", hr.Headers)
	}
}
