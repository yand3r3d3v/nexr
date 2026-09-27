package output

import (
	"errors"
	"fmt"
	"io"

	"github.com/yand3r3d3v/nexr/internal/errs"
)

// HTTPStatuser is implemented by errors that carry an HTTP status code.
type HTTPStatuser interface {
	HTTPStatus() int
}

// ErrorJSON is the document written to stderr for failures in --json mode.
type ErrorJSON struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody describes a failure in JSON output.
type ErrorBody struct {
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	ExitCode   int      `json:"exit_code"`
	HTTPStatus *int     `json:"http_status"`
	Hints      []string `json:"hints"`
}

// PrintError writes err to w, as text or as a JSON document.
func PrintError(w io.Writer, err error, asJSON bool) {
	kind := errs.Classify(err)
	hints := errs.HintsOf(err)
	if hints == nil {
		hints = []string{}
	}
	if asJSON {
		body := ErrorBody{
			Code:     kind.String(),
			Message:  err.Error(),
			ExitCode: kind.ExitCode(),
			Hints:    hints,
		}
		if hs, ok := asHTTPStatuser(err); ok {
			status := hs.HTTPStatus()
			body.HTTPStatus = &status
		}
		_ = WriteJSON(w, ErrorJSON{Error: body}, false)
		return
	}
	fmt.Fprintf(w, "nexr: %s\n", err.Error())
	for _, h := range hints {
		fmt.Fprintf(w, "hint: %s\n", h)
	}
}

func asHTTPStatuser(err error) (HTTPStatuser, bool) {
	var hs HTTPStatuser
	if errors.As(err, &hs) {
		return hs, true
	}
	return nil, false
}
