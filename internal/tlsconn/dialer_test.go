package tlsconn

import (
	"net"
	"reflect"
	"testing"

	"github.com/anglesgirl/ech-proxy-go/internal/dns"
)

func TestCandidateAddressesExcludePreferredIPsForPlainTLS(t *testing.T) {
	dialer := New(0, false, false)
	dialer.customIPs = []string{"104.16.58.46", "172.64.230.224"}
	result := &dns.Result{IPs: []net.IP{net.ParseIP("140.82.112.5")}}

	got := dialer.candidateAddresses(result)
	want := []string{"140.82.112.5:443"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plain TLS candidates = %v, want %v", got, want)
	}
}

func TestCandidateAddressesIncludePreferredIPsForECH(t *testing.T) {
	dialer := New(0, false, false)
	dialer.customIPs = []string{"104.16.58.46"}
	result := &dns.Result{
		IPs: []net.IP{net.ParseIP("104.16.1.2")},
		ECH: &dns.ECHConfig{Config: []byte{1}},
	}

	got := dialer.candidateAddresses(result)
	want := []string{"104.16.58.46:443", "104.16.1.2:443"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ECH candidates = %v, want %v", got, want)
	}
}
