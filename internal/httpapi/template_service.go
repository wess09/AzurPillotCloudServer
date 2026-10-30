package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"ap-cloud-server/internal/templatesync"
)

func (s *Server) handleTemplateEvent(w http.ResponseWriter, r *http.Request) {
	token := s.deps.Cfg.TemplateEventToken
	if token == "" || s.deps.TemplateSync == nil {
		writeError(w, r, apiError{503, "TEMPLATE_SYNC_DISABLED", "模板同步未启用", nil})
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
		writeError(w, r, errUnauthorized())
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		writeError(w, r, apiError{413, "PAYLOAD_TOO_LARGE", "模板事件过大", nil})
		return
	}
	var event templatesync.Event
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		writeError(w, r, errInvalidJSON())
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeError(w, r, errInvalidJSON())
		return
	}
	if err := event.Validate(); err != nil {
		writeError(w, r, apiError{400, "INVALID_TEMPLATE_EVENT", err.Error(), nil})
		return
	}
	digest := sha256.Sum256(body)
	if err := s.deps.TemplateSync.Apply(r.Context(), event, hex.EncodeToString(digest[:])); err != nil {
		if errors.Is(err, templatesync.ErrConflict) {
			writeError(w, r, apiError{409, "EVENT_CONFLICT", err.Error(), nil})
			return
		}
		writeError(w, r, errInternal())
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"ack": event.EventID})
}
