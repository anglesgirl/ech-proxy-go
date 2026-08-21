package echproxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestUploadToR2UsesDirectTransport(t *testing.T) {
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		received <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	oldDefaultTransport := http.DefaultTransport
	http.DefaultTransport = &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return nil, &proxyError{}
		},
	}
	defer func() { http.DefaultTransport = oldDefaultTransport }()

	if !UploadToR2(server.URL, "diagnostics", "test.txt", "access", "secret", "text/plain", "report") {
		t.Fatal("UploadToR2 failed despite a reachable direct endpoint")
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("diagnostic endpoint did not receive the direct request")
	}
}

type proxyError struct{}

func (*proxyError) Error() string { return "default proxy must not be used" }
