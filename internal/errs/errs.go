// Package errs defines the error categories of nexr and maps them to exit codes.
//
// Packages report failures as ordinary Go errors. Errors that know their
// category implement Kinder, so that this package does not need to import them.
// Only the program entry point turns errors into messages and exit codes.
package errs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
)

// Kind is the category of an error. Each kind has a documented exit code.
type Kind int

// Error kinds, see docs/specification.md §7.4.
const (
	KindGeneric Kind = iota
	KindUsage
	KindConfig
	KindAuth
	KindNotFound
	KindPartial
	KindNetwork
	KindTimeout
	KindRejected
	KindInterrupted
)

var kindInfo = map[Kind]struct {
	code int
	name string
}{
	KindGeneric:     {1, "error"},
	KindUsage:       {2, "usage"},
	KindConfig:      {3, "config"},
	KindAuth:        {4, "auth"},
	KindNotFound:    {5, "not_found"},
	KindPartial:     {6, "partial"},
	KindNetwork:     {7, "network"},
	KindTimeout:     {8, "timeout"},
	KindRejected:    {9, "rejected"},
	KindInterrupted: {130, "interrupted"},
}

// ExitCode returns the process exit code for the kind.
func (k Kind) ExitCode() int {
	if info, ok := kindInfo[k]; ok {
		return info.code
	}
	return 1
}

// String returns the machine-readable name used in JSON error output.
func (k Kind) String() string {
	if info, ok := kindInfo[k]; ok {
		return info.name
	}
	return "error"
}

// Kinder is implemented by errors that know their category.
type Kinder interface {
	Kind() Kind
}

// Hinter is implemented by errors that carry hints for the user.
type Hinter interface {
	Hints() []string
}

// Error is an error with a category, a message and optional hints.
type Error struct {
	kind     Kind
	msg      string
	hints    []string
	err      error
	ownHints bool // hide the hints of the wrapped error
}

// New returns an error of the given kind.
func New(kind Kind, format string, args ...any) *Error {
	return &Error{kind: kind, msg: fmt.Sprintf(format, args...)}
}

// Wrap returns an error of the given kind that wraps err. If format is empty,
// the message of err is used.
func Wrap(kind Kind, err error, format string, args ...any) *Error {
	msg := ""
	if format != "" {
		msg = fmt.Sprintf(format, args...)
	}
	return &Error{kind: kind, msg: msg, err: err}
}

// Usage returns a usage error.
func Usage(format string, args ...any) *Error { return New(KindUsage, format, args...) }

// Config returns a configuration error.
func Config(format string, args ...any) *Error { return New(KindConfig, format, args...) }

// NotFound returns a not-found error.
func NotFound(format string, args ...any) *Error { return New(KindNotFound, format, args...) }

// WithHint adds a hint and returns the error for chaining.
func (e *Error) WithHint(format string, args ...any) *Error {
	e.hints = append(e.hints, fmt.Sprintf(format, args...))
	return e
}

// ReplaceHints drops the hints of the wrapped error, for a caller that knows
// better advice, and returns e.
func (e *Error) ReplaceHints() *Error {
	e.ownHints = true
	return e
}

func (e *Error) Error() string {
	switch {
	case e.msg != "" && e.err != nil:
		return e.msg + ": " + e.err.Error()
	case e.msg != "":
		return e.msg
	case e.err != nil:
		return e.err.Error()
	default:
		return e.kind.String()
	}
}

// Unwrap returns the wrapped error.
func (e *Error) Unwrap() error { return e.err }

// Kind returns the category of the error.
func (e *Error) Kind() Kind { return e.kind }

// Hints returns the hints of the error and of the errors it wraps.
func (e *Error) Hints() []string {
	hints := append([]string(nil), e.hints...)
	var inner Hinter
	if !e.ownHints && e.err != nil && errors.As(e.err, &inner) {
		hints = append(hints, inner.Hints()...)
	}
	return hints
}

// Classify returns the category of err.
func Classify(err error) Kind {
	if err == nil {
		return KindGeneric
	}
	// The first explicit, non-generic category in the chain wins, so that a
	// generic wrapper does not hide a more specific cause.
	if k, ok := firstKind(err); ok {
		return k
	}
	switch {
	case errors.Is(err, context.Canceled):
		return KindInterrupted
	case errors.Is(err, context.DeadlineExceeded):
		return KindTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return KindTimeout
	}
	if isNetworkError(err) {
		return KindNetwork
	}
	return KindGeneric
}

func firstKind(err error) (Kind, bool) {
	for err != nil {
		if k, ok := err.(Kinder); ok && k.Kind() != KindGeneric {
			return k.Kind(), true
		}
		switch x := err.(type) { //nolint:errorlint // walks the chain one error at a time
		case interface{ Unwrap() error }:
			err = x.Unwrap()
		case interface{ Unwrap() []error }:
			for _, sub := range x.Unwrap() {
				if k, ok := firstKind(sub); ok {
					return k, true
				}
			}
			return 0, false
		default:
			return 0, false
		}
	}
	return 0, false
}

func isNetworkError(err error) bool {
	var (
		urlErr *url.Error
		opErr  *net.OpError
		dnsErr *net.DNSError
		recErr tls.RecordHeaderError
	)
	// Every transport-level failure of net/http is reported as *url.Error.
	return errors.As(err, &urlErr) || errors.As(err, &opErr) || errors.As(err, &dnsErr) ||
		errors.As(err, &recErr) || IsTLSError(err)
}

// IsTLSError reports whether err is caused by certificate verification.
func IsTLSError(err error) bool {
	var (
		unknown  x509.UnknownAuthorityError
		hostErr  x509.HostnameError
		certErr  x509.CertificateInvalidError
		verifErr *tls.CertificateVerificationError
	)
	return errors.As(err, &unknown) || errors.As(err, &hostErr) || errors.As(err, &certErr) ||
		errors.As(err, &verifErr)
}

// ExitCode returns the exit code for err (0 for nil).
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	return Classify(err).ExitCode()
}

// HintsOf collects the hints attached to err.
func HintsOf(err error) []string {
	var h Hinter
	if errors.As(err, &h) {
		return h.Hints()
	}
	return nil
}

// Bulk summarises the failures of a bulk operation of total items, e.g.
// "2 of 1000 uploads failed". A single item keeps its own error. The kind is
// that of the failures when all items failed the same way, else KindPartial.
func Bulk(verb string, total int, failures []error) error {
	if len(failures) == 0 {
		return nil
	}
	kind := Classify(failures[0])
	same := len(failures) == total
	for _, err := range failures[1:] {
		if Classify(err) != kind {
			same = false
		}
	}
	if total == 1 {
		return failures[0]
	}
	if !same {
		kind = KindPartial
	}
	return New(kind, "%d of %d %s failed", len(failures), total, verb)
}
