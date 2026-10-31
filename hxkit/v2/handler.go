package v2

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

// Handler renders one representation, mutation result, or stream event.
// It returns the component to send, or an error carrying an HTTP status
// code (see Error). A nil component with a nil error is a framework 500:
// representations always owe the client a body; mutations use nil to ask
// for a 303 back to the resource path.
type Handler func(c echo.Context) (templ.Component, error)

// Error is how a Handler reports a status code plus a message. The
// framework maps it onto the canonical HTMX wire shape (see Errors), so
// handlers never set HX- headers or status codes themselves.
type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d: %s", e.Code, e.Message)
}

// E builds an Error for a Handler to return.
func E(code int, message string) *Error {
	return &Error{Code: code, Message: message}
}

// statusFromErr extracts the wire status code and message from a Handler
// error. It accepts *Error and *echo.HTTPError; anything else is a 500
// so handler bugs never leak details under a misleading code.
func statusFromErr(err error) (int, string) {
	var he *Error
	if errors.As(err, &he) && he != nil {
		return normalizeCode(he.Code), he.Message
	}
	var ehe *echo.HTTPError
	if errors.As(err, &ehe) && ehe != nil {
		return normalizeCode(ehe.Code), fmt.Sprintf("%v", ehe.Message)
	}
	return http.StatusInternalServerError, "internal error"
}

func normalizeCode(code int) int {
	if code < 400 || code > 599 {
		return http.StatusInternalServerError
	}
	return code
}
