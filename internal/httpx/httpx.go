// Package httpx builds the HTTP client used for all requests to Nexus.
//
// The client adds, in this order: retries of idempotent requests, the
// User-Agent header, pre-emptive Basic authentication for the configured hosts,
// and debug logging.
package httpx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yand3r3d3v/nexr/internal/errs"
)

// Options configure the client.
type Options struct {
	Insecure   bool
	CAFile     string
	ClientCert string
	ClientKey  string

	Retries   int
	RetryBase time.Duration // first backoff delay; defaults to 500ms

	UserAgent string
	Username  string
	Password  func() (string, error) // resolved lazily, only when a request needs it
	// AuthURLs are the base URLs that may receive credentials. Requests to
	// any other scheme, host or port are sent without them.
	AuthURLs []string

	Logger     *slog.Logger // nil disables logging
	LogDetails bool         // log headers and truncated JSON bodies (-vv)

	// Sleep waits between retries; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
	// Transport replaces the network transport, for tests. The TLS options
	// are ignored when it is set.
	Transport http.RoundTripper
}

// NewClient returns an HTTP client configured with opts.
func NewClient(opts Options) (*http.Client, error) {
	if opts.Transport != nil {
		return &http.Client{Transport: Chain(opts.Transport, opts)}, nil
	}
	tlsCfg, err := tlsConfig(opts)
	if err != nil {
		return nil, err
	}
	base := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
	}
	return &http.Client{Transport: Chain(base, opts)}, nil
}

// Chain wraps next with the retry, user-agent, auth and logging layers.
func Chain(next http.RoundTripper, opts Options) http.RoundTripper {
	rt := next
	if opts.Logger != nil {
		rt = &logTransport{next: rt, log: opts.Logger, details: opts.LogDetails}
	}
	if opts.Username != "" {
		hosts := map[string]bool{}
		for _, u := range opts.AuthURLs {
			if pu, err := url.Parse(u); err == nil {
				hosts[origin(pu)] = true
			}
		}
		pw := opts.Password
		if pw == nil {
			pw = func() (string, error) { return "", nil }
		}
		rt = &authTransport{next: rt, user: opts.Username, password: pw, origins: hosts}
	}
	if opts.UserAgent != "" {
		rt = &uaTransport{next: rt, ua: opts.UserAgent}
	}
	base := opts.RetryBase
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	return &retryTransport{next: rt, retries: opts.Retries, base: base, sleep: sleep, log: opts.Logger}
}

func tlsConfig(opts Options) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: opts.Insecure} //nolint:gosec // only with --insecure
	if opts.CAFile != "" {
		pem, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, errs.Wrap(errs.KindConfig, err, "cannot read CA bundle")
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errs.Config("no PEM certificates found in %s", opts.CAFile)
		}
		cfg.RootCAs = pool
	}
	if opts.ClientCert != "" || opts.ClientKey != "" {
		cert, err := tls.LoadX509KeyPair(opts.ClientCert, opts.ClientKey)
		if err != nil {
			return nil, errs.Wrap(errs.KindConfig, err, "cannot load the client certificate")
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// origin returns scheme://host:port with an explicit port.
func origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[strings.ToLower(u.Scheme)]
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

type uaTransport struct {
	next http.RoundTripper
	ua   string
}

func (t *uaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") != "" {
		return t.next.RoundTrip(req)
	}
	r := req.Clone(req.Context())
	r.Header.Set("User-Agent", t.ua)
	return t.next.RoundTrip(r)
}

type authTransport struct {
	next     http.RoundTripper
	user     string
	password func() (string, error)
	origins  map[string]bool
}

type noAuthKey struct{}

// WithoutAuth returns a context whose requests are sent without credentials.
func WithoutAuth(ctx context.Context) context.Context {
	return context.WithValue(ctx, noAuthKey{}, true)
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.origins[origin(req.URL)] || req.Header.Get("Authorization") != "" || req.Context().Value(noAuthKey{}) != nil {
		return t.next.RoundTrip(req)
	}
	pw, err := t.password()
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.SetBasicAuth(t.user, pw)
	return t.next.RoundTrip(r)
}

type attemptKey struct{}

type retryTransport struct {
	next    http.RoundTripper
	retries int
	base    time.Duration
	sleep   func(ctx context.Context, d time.Duration) error
	log     *slog.Logger
}

func idempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	}
	return false
}

type finalStatusKey struct{}

// WithFinalStatus marks responses with the given status codes as final for the
// requests made with the returned context: they are returned to the caller
// instead of being retried. Health checks use it for 503, which there means
// "not available" rather than "try again". Network errors are still retried.
func WithFinalStatus(ctx context.Context, codes ...int) context.Context {
	return context.WithValue(ctx, finalStatusKey{}, codes)
}

func finalStatus(ctx context.Context, resp *http.Response) bool {
	codes, _ := ctx.Value(finalStatusKey{}).([]int)
	return resp != nil && slices.Contains(codes, resp.StatusCode)
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.retries <= 0 || !idempotent(req.Method) {
		return t.next.RoundTrip(req)
	}
	hasBody := req.Body != nil && req.Body != http.NoBody
	for attempt := 1; ; attempt++ {
		r := req.WithContext(context.WithValue(req.Context(), attemptKey{}, attempt))
		if attempt > 1 && hasBody {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			r.Body = body
		}
		resp, err := t.next.RoundTrip(r)
		if attempt > t.retries || !retryable(resp, err) || finalStatus(req.Context(), resp) ||
			AuthThrottled(resp) || (hasBody && req.GetBody == nil) {
			return resp, err
		}
		delay := backoff(t.base, attempt)
		if resp != nil {
			if ra, ok := retryAfter(resp.Header.Get("Retry-After")); ok {
				delay = min(ra, time.Minute)
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
		}
		if t.log != nil {
			reason := ""
			if err != nil {
				reason = err.Error()
			} else {
				reason = resp.Status
			}
			t.log.Debug("retrying request", "method", req.Method, "url", redactURL(req.URL), "attempt", attempt, "reason", reason, "delay", delay.Round(time.Millisecond))
		}
		if err := t.sleep(req.Context(), delay); err != nil {
			return nil, err
		}
	}
}

// AuthThrottled reports whether resp is the answer of the authentication rate
// limiter of Nexus 3.96 and newer: "429 Too many authentication attempts". Such
// a response is never retried: the block ends only after 15 minutes without
// any request for the user, and every request, even with the right password,
// starts that time again; Retry-After does not say when the block ends. The
// body is peeked at, because HTTP/2 has no reason phrase, and stays readable
// for the caller.
func AuthThrottled(resp *http.Response) bool {
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		return false
	}
	if strings.Contains(strings.ToLower(resp.Status), authThrottledText) {
		return true
	}
	peek, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body = &peekedBody{Reader: io.MultiReader(bytes.NewReader(peek), resp.Body), Closer: resp.Body}
	return bytes.Contains(bytes.ToLower(peek), []byte(authThrottledText))
}

const authThrottledText = "too many authentication attempts"

type peekedBody struct {
	io.Reader
	io.Closer
}

func retryable(resp *http.Response, err error) bool {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errs.IsTLSError(err) {
			return false
		}
		var kinded errs.Kinder
		return !errors.As(err, &kinded) // our own errors (e.g. password lookup) are not transient
	}
	switch resp.StatusCode {
	case http.StatusInternalServerError, http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func backoff(base time.Duration, attempt int) time.Duration {
	d := base << (attempt - 1)
	jitter := 0.8 + 0.4*rand.Float64() //nolint:gosec // jitter does not need a secure source
	return time.Duration(float64(d) * jitter)
}

func retryAfter(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type logTransport struct {
	next    http.RoundTripper
	log     *slog.Logger
	details bool
}

var redactedHeaders = map[string]bool{"Authorization": true, "Cookie": true, "Set-Cookie": true, "Proxy-Authorization": true}

func (t *logTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	attempt, _ := req.Context().Value(attemptKey{}).(int)
	if t.details {
		t.log.Debug("request headers", "method", req.Method, "url", redactURL(req.URL), "headers", headerString(req.Header))
	}
	resp, err := t.next.RoundTrip(req)
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		t.log.Debug("http", "method", req.Method, "url", redactURL(req.URL), "attempt", attempt, "error", err.Error(), "duration", elapsed)
		return resp, err
	}
	t.log.Debug("http", "method", req.Method, "url", redactURL(req.URL), "status", resp.StatusCode, "attempt", attempt, "duration", elapsed)
	if t.details {
		t.log.Debug("response headers", "status", resp.StatusCode, "headers", headerString(resp.Header))
		if strings.Contains(resp.Header.Get("Content-Type"), "json") && resp.Body != nil {
			br := bufio.NewReaderSize(resp.Body, 4096)
			peek, _ := br.Peek(2048)
			t.log.Debug("response body (truncated)", "body", string(peek))
			resp.Body = struct {
				io.Reader
				io.Closer
			}{br, resp.Body}
		}
	}
	return resp, err
}

func headerString(h http.Header) string {
	var b strings.Builder
	for k, vs := range h {
		v := strings.Join(vs, ", ")
		if redactedHeaders[http.CanonicalHeaderKey(k)] {
			v = "[redacted]"
		}
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(k + ": " + v)
	}
	return b.String()
}

func redactURL(u *url.URL) string {
	if u.User == nil {
		return u.String()
	}
	c := *u
	c.User = nil
	return c.String()
}

// NewLogger returns a logger that writes debug messages to w without timestamps.
func NewLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}
