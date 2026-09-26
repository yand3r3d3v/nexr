package httpx

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
)

func noSleep(context.Context, time.Duration) error { return nil }

func TestRetriesIdempotentRequests(t *testing.T) {
	var calls atomic.Int32
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c, err := NewClient(Options{Retries: 3, Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/file", bytes.NewReader([]byte("payload")))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || calls.Load() != 3 {
		t.Fatalf("status %d after %d calls", resp.StatusCode, calls.Load())
	}
	for i, b := range bodies {
		if b != "payload" {
			t.Errorf("attempt %d sent body %q", i+1, b)
		}
	}
}

func TestFinalStatusIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c, _ := NewClient(Options{Retries: 3, Sleep: noSleep})

	ctx := WithFinalStatus(context.Background(), http.StatusServiceUnavailable)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || calls.Load() != 1 {
		t.Fatalf("status %d after %d calls, want 503 after 1", resp.StatusCode, calls.Load())
	}
}

func TestAuthThrottleIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/html;charset=utf-8")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "<html><body><p>Too many authentication attempts</p></body></html>")
	}))
	defer srv.Close()
	c, _ := NewClient(Options{Retries: 3, Sleep: func(context.Context, time.Duration) error {
		t.Error("an authentication rate limit must not be retried")
		return nil
	}})
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if calls.Load() != 1 || !strings.Contains(string(body), "Too many authentication attempts") {
		t.Fatalf("%d calls, body %q", calls.Load(), body)
	}

	for _, tt := range []struct {
		status string
		body   string
		want   bool
	}{
		{"429 Too many authentication attempts", "", true}, // HTTP/1.1 reason phrase of Nexus
		{"429 Too Many Requests", "<p>Too many authentication attempts</p>", true},
		{"429 Too Many Requests", "slow down", false},
	} {
		r := &http.Response{StatusCode: http.StatusTooManyRequests, Status: tt.status, Body: io.NopCloser(strings.NewReader(tt.body))}
		if got := AuthThrottled(r); got != tt.want {
			t.Errorf("AuthThrottled(%q, %q) = %v", tt.status, tt.body, got)
		}
		if b, _ := io.ReadAll(r.Body); string(b) != tt.body {
			t.Errorf("the body was not restored: %q", b)
		}
	}
}

func TestNoRetryForPostAndGivesUp(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c, _ := NewClient(Options{Retries: 2, Sleep: noSleep})

	resp, err := c.Post(srv.URL, "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Fatalf("POST was sent %d times", calls.Load())
	}

	calls.Store(0)
	resp, err = c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || calls.Load() != 3 {
		t.Fatalf("GET: status %d after %d calls, want 502 after 3", resp.StatusCode, calls.Load())
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	var slept []time.Duration
	c, _ := NewClient(Options{Retries: 3, Sleep: func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}})
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(slept) != 1 || slept[0] != 7*time.Second {
		t.Fatalf("slept %v, want [7s]", slept)
	}
}

type stubRT struct {
	errs  []error
	calls int
}

func (s *stubRT) RoundTrip(r *http.Request) (*http.Response, error) {
	s.calls++
	if s.calls <= len(s.errs) {
		return nil, s.errs[s.calls-1]
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
}

func TestRetryOnNetworkErrorsOnly(t *testing.T) {
	stub := &stubRT{errs: []error{errors.New("connection reset by peer")}}
	c := &http.Client{Transport: Chain(stub, Options{Retries: 2, Sleep: noSleep})}
	resp, err := c.Get("http://nexus.example.com/")
	if err != nil {
		t.Fatalf("network error was not retried: %v", err)
	}
	resp.Body.Close()
	if stub.calls != 2 {
		t.Fatalf("calls = %d, want 2", stub.calls)
	}

	stub = &stubRT{errs: []error{errs.Config("bad password source")}}
	c = &http.Client{Transport: Chain(stub, Options{Retries: 2, Sleep: noSleep})}
	_, err = c.Get("http://nexus.example.com/")
	if errs.Classify(err) != errs.KindConfig || stub.calls != 1 {
		t.Fatalf("config error: err=%v calls=%d", err, stub.calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stub = &stubRT{}
	c = &http.Client{Transport: Chain(stub, Options{Retries: 2, Sleep: sleepCtx})}
	stub.errs = []error{errors.New("connection refused")}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://nexus.example.com/", nil)
	if _, err := c.Do(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context: err = %v", err)
	}
}

func TestAuthOnlyForConfiguredOrigin(t *testing.T) {
	var otherAuth string
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		otherAuth = r.Header.Get("Authorization")
	}))
	defer other.Close()
	var nexusAuth, ua string
	nexus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nexusAuth, ua = r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, other.URL+"/landing", http.StatusFound)
		}
	}))
	defer nexus.Close()

	c, _ := NewClient(Options{
		Username: "alice", Password: func() (string, error) { return "s3cret", nil },
		AuthURLs: []string{nexus.URL + "/nexus"}, UserAgent: "nexr/test",
	})
	resp, err := c.Get(nexus.URL + "/service/rest/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if nexusAuth != "Basic YWxpY2U6czNjcmV0" || ua != "nexr/test" {
		t.Fatalf("nexus got auth %q ua %q", nexusAuth, ua)
	}
	resp, err = c.Get(nexus.URL + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if otherAuth != "" {
		t.Fatalf("credentials leaked to another origin on redirect: %q", otherAuth)
	}
	resp, err = c.Get(other.URL + "/direct")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if otherAuth != "" {
		t.Fatalf("credentials sent to another origin: %q", otherAuth)
	}
}

func TestWithoutAuth(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
	}))
	defer srv.Close()
	c, _ := NewClient(Options{
		Username: "alice", AuthURLs: []string{srv.URL},
		Password: func() (string, error) { return "s3cret", nil },
	})
	for _, ctx := range []context.Context{WithoutAuth(context.Background()), context.Background()} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if len(auth) != 2 || auth[0] != "" || auth[1] == "" {
		t.Fatalf("Authorization headers = %q, want none, then Basic", auth)
	}
}

func TestPasswordErrorStopsRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("request must not be sent")
	}))
	defer srv.Close()
	c, _ := NewClient(Options{
		Username: "alice", AuthURLs: []string{srv.URL}, Retries: 3, Sleep: noSleep,
		Password: func() (string, error) { return "", errs.Config("password_env MY_PW is not set") },
	})
	_, err := c.Get(srv.URL)
	if errs.Classify(err) != errs.KindConfig {
		t.Fatalf("err = %v, want config error", err)
	}
}

func TestLoggingRedactsSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "NXSESSIONID=abc")
		_, _ = io.WriteString(w, `{"items":[]}`)
	}))
	defer srv.Close()
	var logs bytes.Buffer
	c, _ := NewClient(Options{
		Username: "alice", Password: func() (string, error) { return "s3cret", nil }, AuthURLs: []string{srv.URL},
		Logger: NewLogger(&logs), LogDetails: true,
	})
	resp, err := c.Get(srv.URL + "/service/rest/v1/repositories")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != `{"items":[]}` {
		t.Fatalf("body after logging = %q", body)
	}
	out := logs.String()
	if strings.Contains(out, "s3cret") || strings.Contains(out, "YWxpY2U6czNjcmV0") || strings.Contains(out, "abc") {
		t.Fatalf("log leaks secrets:\n%s", out)
	}
	for _, want := range []string{"status=200", "Authorization: [redacted]", `{\"items\":[]}`} {
		if !strings.Contains(out, want) {
			t.Errorf("log misses %q:\n%s", want, out)
		}
	}
}

func TestCustomCAAndInsecure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	plain, _ := NewClient(Options{})
	if _, err := plain.Get(srv.URL); !errs.IsTLSError(err) || errs.Classify(err) != errs.KindNetwork {
		t.Fatalf("untrusted certificate: err = %v", err)
	}

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	writePEM(t, caFile, "CERTIFICATE", srv.Certificate().Raw)
	trusted, err := NewClient(Options{CAFile: caFile})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := trusted.Get(srv.URL)
	if err != nil {
		t.Fatalf("with CA bundle: %v", err)
	}
	resp.Body.Close()

	insecure, _ := NewClient(Options{Insecure: true})
	resp, err = insecure.Get(srv.URL)
	if err != nil {
		t.Fatalf("insecure: %v", err)
	}
	resp.Body.Close()

	empty := filepath.Join(t.TempDir(), "empty.pem")
	_ = os.WriteFile(empty, []byte("not a certificate"), 0o600)
	if _, err := NewClient(Options{CAFile: empty}); errs.Classify(err) != errs.KindConfig {
		t.Fatalf("bad CA bundle: err = %v", err)
	}
}

func TestClientCertificate(t *testing.T) {
	certPEM, keyPEM, cert := selfSigned(t)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.TLS.PeerCertificates[0].Subject.CommonName)
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()

	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "client.pem"), filepath.Join(dir, "client.key")
	_ = os.WriteFile(certFile, certPEM, 0o600)
	_ = os.WriteFile(keyFile, keyPEM, 0o600)

	c, err := NewClient(Options{Insecure: true, ClientCert: certFile, ClientKey: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("with client certificate: %v", err)
	}
	cn, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(cn) != "nexr-test-client" {
		t.Fatalf("server saw %q", cn)
	}
	if _, err := NewClient(Options{ClientCert: certFile, ClientKey: filepath.Join(dir, "missing")}); errs.Classify(err) != errs.KindConfig {
		t.Fatalf("missing key: err = %v", err)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	if d, ok := retryAfter("3"); !ok || d != 3*time.Second {
		t.Errorf("retryAfter(3) = %v %v", d, ok)
	}
	if _, ok := retryAfter("soon"); ok {
		t.Error("retryAfter(soon) parsed")
	}
	future := time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)
	if d, ok := retryAfter(future); !ok || d <= time.Minute {
		t.Errorf("retryAfter(date) = %v %v", d, ok)
	}
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func selfSigned(t *testing.T) (certPEM, keyPEM []byte, cert *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "nexr-test-client"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ = x509.ParseCertificate(der)
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), cert
}
