package nexus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/yand3r3d3v/nexr/internal/errs"
	"github.com/yand3r3d3v/nexr/internal/httpx"
)

// APIError is a non-2xx response from Nexus.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Detail     string // server message, when it adds information
	FaultID    string // "siesta-faultid" for correlation with the server log
	Validation []ValidationError
	// AuthThrottled is set for "429 Too many authentication attempts": Nexus
	// 3.96+ blocks sign-ins of a user for a while after failed attempts.
	AuthThrottled bool
}

// ValidationError is one entry of a Nexus validation error array.
type ValidationError struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: %d %s", e.Method, e.Path, e.StatusCode, http.StatusText(e.StatusCode))
	if e.Detail != "" {
		b.WriteString(": " + e.Detail)
	}
	if e.FaultID != "" {
		b.WriteString(" (fault id " + e.FaultID + ")")
	}
	return b.String()
}

// HTTPStatus returns the status code.
func (e *APIError) HTTPStatus() int { return e.StatusCode }

// Kind maps the status code to an error category.
func (e *APIError) Kind() errs.Kind {
	if e.AuthThrottled {
		return errs.KindAuth
	}
	switch e.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return errs.KindAuth
	case http.StatusNotFound:
		return errs.KindNotFound
	case http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusConflict,
		http.StatusPreconditionFailed, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return errs.KindRejected
	}
	return errs.KindGeneric
}

// Hints suggests what to do about the error.
func (e *APIError) Hints() []string {
	if e.AuthThrottled {
		return []string{
			"Nexus blocks a user after repeated failed sign-ins; the block ends after 15 minutes without any request " +
				"for that user (the default), and every request, even with the right password, starts that time again",
			"an administrator can lift the block at once by updating the user or changing its password",
		}
	}
	switch e.StatusCode {
	case http.StatusUnauthorized:
		return []string{"check the user and password; \"nexr config view\" shows where they come from"}
	case http.StatusForbidden:
		return []string{"the user lacks the Nexus privilege needed for this operation"}
	}
	return nil
}

func decodeError(resp *http.Response) *APIError {
	e := &APIError{StatusCode: resp.StatusCode, AuthThrottled: httpx.AuthThrottled(resp)}
	if resp.Request != nil {
		e.Method, e.Path = resp.Request.Method, resp.Request.URL.Path
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	body = bytes.TrimSpace(body)
	var details []string
	switch {
	case len(body) > 0 && body[0] == '{':
		var f struct {
			FaultID string `json:"siesta-faultid"`
			Message string `json:"message"`
			Errors  []struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		if json.Unmarshal(body, &f) == nil {
			e.FaultID = f.FaultID
			details = append(details, f.Message)
			for _, re := range f.Errors {
				details = append(details, strings.TrimSpace(re.Code+" "+re.Message))
			}
		}
	case len(body) > 0 && body[0] == '[':
		if json.Unmarshal(body, &e.Validation) == nil {
			for _, v := range e.Validation {
				details = append(details, v.Message)
			}
		}
	case strings.Contains(resp.Header.Get("Content-Type"), "html"):
		// HTML error pages carry no useful detail, except this one.
		if e.AuthThrottled {
			details = append(details, "Too many authentication attempts")
		}
	case len(body) > 0:
		line, _, _ := strings.Cut(string(body), "\n")
		details = append(details, line)
	}
	reason := strings.TrimSpace(strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)))
	details = append(details, reason)

	seen := map[string]bool{"": true, http.StatusText(resp.StatusCode): true, e.Path: true, strings.TrimPrefix(e.Path, "/"): true}
	var kept []string
	for _, d := range details {
		d = strings.TrimSpace(d)
		if len(d) > 300 {
			d = d[:300] + "…"
		}
		if seen[d] || strings.HasSuffix(e.Path, d) {
			continue
		}
		seen[d] = true
		kept = append(kept, d)
	}
	e.Detail = strings.Join(kept, "; ")
	return e
}
