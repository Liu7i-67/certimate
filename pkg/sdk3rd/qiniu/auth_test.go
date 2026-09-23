package qiniu

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qiniu/go-sdk/v7/auth"
)

const (
	testAccessKey = "test-access-key"
	testSecretKey = "test-secret-key"
)

// hmacSha1Base64Url reproduces go-sdk auth.Credentials.Sign:
// base64.URLEncoding(hmac-sha1(secretKey, data)).
func hmacSha1Base64Url(secretKey string, data []byte) string {
	h := hmac.New(sha1.New, []byte(secretKey))
	h.Write(data)
	return base64.URLEncoding.EncodeToString(h.Sum(nil))
}

// expectedAuthorizationV2 recomputes the expected "Qiniu <ak>:<sig>" value for a
// request received by the test server, mirroring go-sdk auth.collectDataV2:
//
//	<METHOD> <path>[?<query>]\nHost: <host>\nContent-Type: <ct>\n\n[<body>]
//
// where <ct> falls back to "application/x-www-form-urlencoded" when the request
// carries no Content-Type, and the body participates in the signature only when
// the Content-Type is form-urlencoded or JSON (auth.incBodyV2).
func expectedAuthorizationV2(req *http.Request, body []byte) string {
	contentType := req.Header.Get("Content-Type")
	incBody := req.Body != nil &&
		(contentType == "application/x-www-form-urlencoded" || contentType == "application/json")
	if contentType == "" {
		contentType = "application/x-www-form-urlencoded"
	}

	s := fmt.Sprintf("%s %s", req.Method, req.URL.Path)
	if req.URL.RawQuery != "" {
		s += "?" + req.URL.RawQuery
	}
	s += "\nHost: " + req.Host + "\n"
	s += fmt.Sprintf("Content-Type: %s\n", contentType)
	s += "\n"
	if incBody {
		s += string(body)
	}

	return "Qiniu " + testAccessKey + ":" + hmacSha1Base64Url(testSecretKey, []byte(s))
}

// newSignatureVerifyingServer starts a local test server that recomputes the
// Qiniu V2 signature from the request it actually received (including the Host
// header) and reports a test error on any mismatch. It records the observed
// Host and Authorization headers for later assertions.
func newSignatureVerifyingServer(t *testing.T) (ts *httptest.Server, observed func() (host, authorization string)) {
	t.Helper()

	var (
		gotHost          string
		gotAuthorization string
	)

	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotHost = r.Host
		gotAuthorization = r.Header.Get("Authorization")

		if want := expectedAuthorizationV2(r, body); gotAuthorization != want {
			t.Errorf("Authorization mismatch:\n got: %q\nwant: %q", gotAuthorization, want)
		}
		if gotHost == "" {
			t.Error("request must carry a non-empty Host header")
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)

	return ts, func() (string, string) { return gotHost, gotAuthorization }
}

// doSigned sends req through the signing transport, as the qiniu client does,
// and returns the response body.
func doSigned(t *testing.T, req *http.Request) []byte {
	t.Helper()

	client := &http.Client{Transport: newTransport(auth.New(testAccessKey, testSecretKey), nil)}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to do request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status code: %d, body: %s", resp.StatusCode, body)
	}
	return body
}

func TestTransport_RoundTrip_SignRequestV2_GET(t *testing.T) {
	ts, observed := newSignatureVerifyingServer(t)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/domain/example.com?https=true", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	// Simulate the qiniu client.newRequest behavior on affected toolchains,
	// where req.Host is left empty at signing time (net/http only derives the
	// Host header from the URL when the request is actually sent).
	req.Host = ""

	doSigned(t, req)

	gotHost, gotAuthorization := observed()
	if gotHost == "" {
		t.Fatal("server received an empty Host header")
	}
	if want := strings.TrimPrefix(ts.URL, "http://"); gotHost != want {
		t.Errorf("Host header mismatch:\n got: %q\nwant: %q", gotHost, want)
	}
	if !strings.HasPrefix(gotAuthorization, "Qiniu "+testAccessKey+":") {
		t.Fatalf("unexpected Authorization header: %q", gotAuthorization)
	}
}

func TestTransport_RoundTrip_SignRequestV2_POSTJSON(t *testing.T) {
	ts, observed := newSignatureVerifyingServer(t)

	const payload = `{"name":"example.com"}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/domain/example.com", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Same as above: mimic client.newRequest with an empty req.Host.
	req.Host = ""

	doSigned(t, req)

	gotHost, gotAuthorization := observed()
	if gotHost == "" {
		t.Fatal("server received an empty Host header")
	}
	if want := strings.TrimPrefix(ts.URL, "http://"); gotHost != want {
		t.Errorf("Host header mismatch:\n got: %q\nwant: %q", gotHost, want)
	}
	if !strings.HasPrefix(gotAuthorization, "Qiniu "+testAccessKey+":") {
		t.Fatalf("unexpected Authorization header: %q", gotAuthorization)
	}
}
