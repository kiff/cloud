package handlers

import (
	"context"
	"errors"
	"net/http"
)

// clientLeft: err is r's own context ending because the caller went away
// (an app closed the screen mid-read), not a failure of KIFF's: nothing to
// log as one. A deadline KIFF set, or any other error, still is.
func clientLeft(r *http.Request, err error) bool {
	return errors.Is(err, context.Canceled) && errors.Is(r.Context().Err(), context.Canceled)
}
