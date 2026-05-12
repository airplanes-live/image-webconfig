package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/airplanes-live/image/webconfig/internal/wifi"
)

const (
	// wifiHelperTimeout caps the apl-wifi child's wall time. The helper's
	// own connect-before-save budget is ~65s (timeout 60 on nmcli + ~5s
	// rollback); the outer 70s gives the helper a few seconds to surface
	// its rollback envelope before exec.CommandContext SIGKILLs the child.
	wifiHelperTimeout = 70 * time.Second

	// wifiBodyLimit caps incoming JSON request bodies. A worst-case payload
	// (id + ssid + 64-hex psk + hidden + priority + test flag + force flags)
	// stays under 512 bytes; 2 KiB is comfortable headroom without inviting
	// memory-exhaustion floods.
	wifiBodyLimit = 2048
)

// invokeWifi pipes raw JSON body bytes through the given privileged argv and
// parses the helper's stdout to map the envelope status into an HTTP code.
// Returns the helper's full stdout (forwarded verbatim to the browser),
// the HTTP status, and a non-nil error only on runner-layer failures
// (binary missing, stdout unparseable, empty stdout). The helper's own
// rejection paths land here as (stdout, 400, nil).
func (s *Server) invokeWifi(ctx context.Context, argv []string, body []byte) ([]byte, int, error) {
	cctx, cancel := context.WithTimeout(ctx, wifiHelperTimeout)
	defer cancel()
	res, _ := s.stdinRunner(cctx, argv, bytes.NewReader(body))
	if len(res.Stdout) == 0 {
		return nil, 0, fmt.Errorf("apl-wifi %v: empty stdout (stderr=%q)", argv, strings.TrimSpace(string(res.Stderr)))
	}
	status, perr := wifi.Parse(res.Stdout)
	if perr != nil {
		return nil, 0, fmt.Errorf("apl-wifi %v: parse stdout: %w (stdout=%q)", argv, perr, res.Stdout)
	}
	return res.Stdout, wifi.HTTPStatus(status), nil
}

// writeWifiResponse forwards the helper's JSON envelope to the browser with
// no-store cache headers and the HTTP status the envelope maps to.
func (s *Server) writeWifiResponse(w http.ResponseWriter, body []byte, httpStatus int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(httpStatus)
	_, _ = w.Write(body)
	if len(body) == 0 || body[len(body)-1] != '\n' {
		_, _ = w.Write([]byte("\n"))
	}
}

// readWifiBody reads the request body with the wifi-specific size cap and
// content-type enforcement. Returns the raw bytes for forwarding (no struct
// validation here — the helper owns it). Empty body is OK; the helper
// rejects an empty body for mutating subcommands that require fields.
func readWifiBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.ContentLength == 0 {
		// Allow empty body — DELETE often has none.
		return []byte("{}"), nil
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		return nil, errBadContentType
	}
	r.Body = http.MaxBytesReader(w, r.Body, wifiBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return []byte("{}"), nil
	}
	return body, nil
}

// injectID merges {"id": id} into a JSON object body, overwriting any client-
// supplied "id" field so the URL path is the authoritative identifier. The
// body must already be a JSON object or empty.
func injectID(body []byte, id string) ([]byte, error) {
	m := map[string]json.RawMessage{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, errors.New("request body must be a JSON object")
		}
	}
	idJSON, _ := json.Marshal(id)
	m["id"] = idJSON
	return json.Marshal(m)
}

// --- read-only handlers ---------------------------------------------------

func (s *Server) handleWifiList(w http.ResponseWriter, r *http.Request) {
	body, httpStatus, err := s.invokeWifi(r.Context(), s.priv.WifiList, nil)
	if err != nil {
		log.Printf("wifi list: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "wifi list failed")
		return
	}
	s.writeWifiResponse(w, body, httpStatus)
}

func (s *Server) handleWifiStatus(w http.ResponseWriter, r *http.Request) {
	body, httpStatus, err := s.invokeWifi(r.Context(), s.priv.WifiStatus, nil)
	if err != nil {
		log.Printf("wifi status: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "wifi status failed")
		return
	}
	s.writeWifiResponse(w, body, httpStatus)
}

// --- mutating handlers ----------------------------------------------------

func (s *Server) handleWifiAdd(w http.ResponseWriter, r *http.Request) {
	body, err := readWifiBody(w, r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, httpStatus, ierr := s.invokeWifi(r.Context(), s.priv.WifiAdd, body)
	if ierr != nil {
		log.Printf("wifi add: %v", ierr)
		writeJSONError(w, http.StatusInternalServerError, "wifi add failed")
		return
	}
	s.writeWifiResponse(w, resp, httpStatus)
}

func (s *Server) handleWifiUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !wifi.ValidID(id) {
		writeJSONError(w, http.StatusBadRequest, "invalid id; expected airplanes-config-wifi or airplanes-wifi-<slug>")
		return
	}
	body, err := readWifiBody(w, r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	body, err = injectID(body, id)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, httpStatus, ierr := s.invokeWifi(r.Context(), s.priv.WifiUpdate, body)
	if ierr != nil {
		log.Printf("wifi update: %v", ierr)
		writeJSONError(w, http.StatusInternalServerError, "wifi update failed")
		return
	}
	s.writeWifiResponse(w, resp, httpStatus)
}

func (s *Server) handleWifiDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !wifi.ValidID(id) {
		writeJSONError(w, http.StatusBadRequest, "invalid id; expected airplanes-config-wifi or airplanes-wifi-<slug>")
		return
	}
	body, err := readWifiBody(w, r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	body, err = injectID(body, id)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, httpStatus, ierr := s.invokeWifi(r.Context(), s.priv.WifiDelete, body)
	if ierr != nil {
		log.Printf("wifi delete: %v", ierr)
		writeJSONError(w, http.StatusInternalServerError, "wifi delete failed")
		return
	}
	s.writeWifiResponse(w, resp, httpStatus)
}

func (s *Server) handleWifiActivate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !wifi.ValidID(id) {
		writeJSONError(w, http.StatusBadRequest, "invalid id; expected airplanes-config-wifi or airplanes-wifi-<slug>")
		return
	}
	// activate ignores any client body, but still go through the same path
	// for consistency. We inject id into an empty object.
	body, err := injectID([]byte("{}"), id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "activate body marshal failed")
		return
	}
	resp, httpStatus, ierr := s.invokeWifi(r.Context(), s.priv.WifiActivate, body)
	if ierr != nil {
		log.Printf("wifi activate: %v", ierr)
		writeJSONError(w, http.StatusInternalServerError, "wifi activate failed")
		return
	}
	s.writeWifiResponse(w, resp, httpStatus)
}

func (s *Server) handleWifiTest(w http.ResponseWriter, r *http.Request) {
	body, err := readWifiBody(w, r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, httpStatus, ierr := s.invokeWifi(r.Context(), s.priv.WifiTest, body)
	if ierr != nil {
		log.Printf("wifi test: %v", ierr)
		writeJSONError(w, http.StatusInternalServerError, "wifi test failed")
		return
	}
	s.writeWifiResponse(w, resp, httpStatus)
}
