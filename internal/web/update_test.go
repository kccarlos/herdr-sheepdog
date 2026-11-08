package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/update"
)

// POST /api/update answers 202 with the status once the update started,
// 409 with the daemon's reason when it cannot, 404 on a daemon that
// cannot update itself.
func TestHandleUpdate(t *testing.T) {
	for _, c := range []struct {
		install func() error
		code    int
		body    string
	}{
		{func() error { return nil }, http.StatusAccepted, `"installing"`},
		{func() error { return errors.New("sheepdog v1.0.0 is up to date") }, http.StatusConflict, "up to date"},
		{nil, http.StatusNotFound, "cannot update itself"},
	} {
		s := &Server{deps: Deps{InstallUpdate: c.install, Update: func() update.Status { return update.Status{State: "installing"} }}}
		rec := httptest.NewRecorder()
		s.handleUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/update", nil))
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("got %d %s, want %d %q", rec.Code, rec.Body, c.code, c.body)
		}
	}
}
