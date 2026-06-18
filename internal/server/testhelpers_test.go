package server

import (
	"bytes"
	"net/http"
	"testing"
)

// readBody drains an http.Response body into a byte slice, failing the test
// on a read error. Shared by the handler tests that assert response bodies
// (combined backup/restore, claim rotate, ...).
func readBody(t *testing.T, r *http.Response) []byte {
	t.Helper()
	b := new(bytes.Buffer)
	if _, err := b.ReadFrom(r.Body); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
