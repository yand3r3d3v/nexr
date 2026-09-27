package errs

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"
)

type kinded struct{ k Kind }

func (e kinded) Error() string { return "kinded" }
func (e kinded) Kind() Kind    { return e.k }

func TestExitCodes(t *testing.T) {
	want := map[Kind]int{
		KindGeneric: 1, KindUsage: 2, KindConfig: 3, KindAuth: 4, KindNotFound: 5,
		KindPartial: 6, KindNetwork: 7, KindTimeout: 8, KindRejected: 9, KindInterrupted: 130,
	}
	for k, code := range want {
		if got := k.ExitCode(); got != code {
			t.Errorf("%v.ExitCode() = %d, want %d", k, got, code)
		}
	}
	if ExitCode(nil) != 0 {
		t.Error("ExitCode(nil) != 0")
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Kind
	}{
		{"plain", errors.New("boom"), KindGeneric},
		{"usage", Usage("bad flag"), KindUsage},
		{"wrapped kinder", fmt.Errorf("ctx: %w", kinded{KindAuth}), KindAuth},
		{"generic wraps network", Wrap(KindGeneric, &url.Error{Op: "Get", URL: "http://x", Err: errors.New("refused")}, "request failed"), KindNetwork},
		{"canceled", fmt.Errorf("x: %w", context.Canceled), KindInterrupted},
		{"deadline", &url.Error{Op: "Get", URL: "http://x", Err: context.DeadlineExceeded}, KindTimeout},
		{"dns", &net.DNSError{Err: "no such host", Name: "nexus.invalid"}, KindNetwork},
		{"tls", &url.Error{Op: "Get", URL: "https://x", Err: x509.UnknownAuthorityError{}}, KindNetwork},
		{"explicit not found", NotFound("repository %q not found", "x"), KindNotFound},
		{"joined", errors.Join(errors.New("a"), Config("bad file")), KindConfig},
		{"joined without kinds", errors.Join(errors.New("a"), errors.New("b")), KindGeneric},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.err); got != tt.want {
				t.Errorf("Classify() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHints(t *testing.T) {
	inner := New(KindAuth, "denied").WithHint("check credentials")
	outer := Wrap(KindGeneric, inner, "listing failed").WithHint("see --help")
	got := HintsOf(outer)
	if len(got) != 2 || got[0] != "see --help" || got[1] != "check credentials" {
		t.Fatalf("HintsOf() = %q", got)
	}
	if Classify(outer) != KindAuth {
		t.Fatalf("Classify(outer) = %v, want auth", Classify(outer))
	}
	if outer.Error() != "listing failed: denied" {
		t.Fatalf("Error() = %q", outer.Error())
	}
	replaced := Wrap(KindAuth, inner, "").ReplaceHints().WithHint("better advice")
	if got := HintsOf(replaced); len(got) != 1 || got[0] != "better advice" {
		t.Fatalf("HintsOf(replaced) = %q", got)
	}
}

func TestUnknownKind(t *testing.T) {
	if k := Kind(99); k.ExitCode() != 1 || k.String() != "error" {
		t.Fatalf("unknown kind: exit %d, name %q", k.ExitCode(), k.String())
	}
}

func TestBulk(t *testing.T) {
	notFound := NotFound("x")
	auth := New(KindAuth, "forbidden")
	if Bulk("uploads", 3, nil) != nil {
		t.Fatal("no failures")
	}
	if err := Bulk("uploads", 1, []error{notFound}); !errors.Is(err, notFound) {
		t.Fatalf("single item: %v", err)
	}
	if err := Bulk("deletions", 2, []error{notFound, notFound}); Classify(err) != KindNotFound || err.Error() != "2 of 2 deletions failed" {
		t.Fatalf("all alike: %v", err)
	}
	if err := Bulk("deletions", 2, []error{notFound, auth}); Classify(err) != KindPartial {
		t.Fatalf("mixed: %v", err)
	}
	if err := Bulk("uploads", 5, []error{notFound}); Classify(err) != KindPartial {
		t.Fatalf("partial: %v", err)
	}
}
