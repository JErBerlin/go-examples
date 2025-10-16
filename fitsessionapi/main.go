package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ErrorResponse struct {
	Error   string         `json:"error"`
	Code    string         `json:"code"`
	Details map[string]any `json:"details,omitempty"`
}

// Domain

type Session struct {
	ID        string     `json:"id"`
	UserID    string     `json:"userID"`
	MachineID string     `json:"machineID"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	SetsCount int        `json:"setsCount"`
}

type SummaryItem struct {
	Date          string  `json:"date"`
	TotalWorkouts int     `json:"totalWorkouts"`
	TotalReps     int     `json:"totalReps"`
	TotalWeightKg float64 `json:"totalWeightKg"`
	AvgHR         *int    `json:"avgHR,omitempty"`
}

type SummaryPage struct {
	Items      []SummaryItem `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// Stores

// SessionStores has saved sessions indexed by id of the session.
// The id of the session has the format "s_" + strconv.FormatInt(now.UnixNano(), 10).
type SessionStore struct {
	mu sync.Mutex
	m  map[string]Session
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		mu: sync.Mutex{},
		m:  make(map[string]Session),
	}
}

// SummaryStores has saved summaries for each user indexed by userID.
type SummaryStore struct {
	mu sync.Mutex
	m  map[string]SummaryItem
}

func NewSummaryStore() *SummaryStore {
	return &SummaryStore{
		mu: sync.Mutex{},
		m:  make(map[string]SummaryItem),
	}
}

// Main
// All in one place for now.

func main() {
	logger := log.New(os.Stdout, "\x1b[32mINFO\x1b[0m ", log.LstdFlags|log.Lshortfile)
	sessionStore := NewSessionStore()

	mux := http.NewServeMux()

	// mux.HandleFunc("POST /sessions", notImplemented)
	mux.HandleFunc("POST /sessions", func(w http.ResponseWriter, r *http.Request) {
		startSession(sessionStore, w, r)
	})

	logger.Println("listening on :8080")
	logger.Fatal(http.ListenAndServe(":8080", mux))
}

// Helpers

func decodeStrictJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	defer r.Body.Close()
	dec.DisallowUnknownFields()

	return dec.Decode(v)
}

func writeErrorResponse(w http.ResponseWriter, rerr ErrorResponse, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(rerr) // ignoring possible encode error
}

func writeJSONResponse(w http.ResponseWriter, payload any, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload) // ignoring possible encode error
}

// Logic
// Session

func (s *SessionStore) CreateSession(ctx context.Context, userID, machineID string, now time.Time) (Session, error) {
	id := "s_" + strconv.FormatInt(now.UnixNano(), 10)

	session := Session{
		ID:        id,
		UserID:    userID,
		MachineID: machineID,
		StartedAt: now.UTC(),
		EndedAt:   nil,
		SetsCount: 0,
	}

	s.mu.Lock()
	s.m[id] = session
	s.mu.Unlock()

	return session, nil
}

// Stub implementation (real logic TBD)
func (s *SummaryStore) GetSummaries(ctx context.Context, userID, cursor string, limit int) (items []SummaryItem, nextCursor string, found bool) {
	_ = ctx // not used by now
	_ = cursor
	_ = limit

	s.mu.Lock()
	defer s.mu.Unlock()

	item, found := s.m[userID]
	if !found {
		return nil, "", false
	}

	items = []SummaryItem{item}

	// ignore limit and return only one item and empty next cursor by now
	return
}

func ptr[T any](v T) *T { return &v }

// Handlers

// Sessions
type startReq struct {
	UserID    string `json:"userID"`
	MachineID string `json:"machineID"`
}

func startSession(store *SessionStore, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_ = ctx // not used for now

	var sreq startReq
	if err := decodeStrictJSON(r, &sreq); err != nil {
		writeErrorResponse(w, ErrorResponse{
			Error:   "validation_error",
			Code:    "400",
			Details: map[string]any{"message": err.Error()},
		}, http.StatusBadRequest)
		return
	}

	missing := map[string]any{}
	if strings.TrimSpace(sreq.UserID) == "" {
		missing["userID"] = "required"
	}
	if strings.TrimSpace(sreq.MachineID) == "" {
		missing["machineID"] = "required"
	}
	if len(missing) > 0 {
		writeErrorResponse(w, ErrorResponse{
			Error:   "validation_error",
			Code:    "400",
			Details: missing,
		}, http.StatusBadRequest)
		return
	}

	sess, err := store.CreateSession(r.Context(), sreq.UserID, sreq.MachineID, time.Now())
	if err != nil {
		writeErrorResponse(w, ErrorResponse{Error: "internal_error", Code: "500"}, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Location", "/sessions/"+sess.ID)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(sess) // ignoring possible encode error
}

// Summaries

func getSummaries(store *SummaryStore, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_ = ctx // not used for now

	userID := r.PathValue("userID")
	if userID == "" {
		writeErrorResponse(w, ErrorResponse{
			Error: "validation_error",
			Code:  "400",
			Details: map[string]any{
				"userID": "missing",
			},
		}, http.StatusBadRequest)
		return
	}

	cursorStr := r.URL.Query().Get("cursor")
	if ok := validateCursor(cursorStr); !ok {
		writeErrorResponse(w, ErrorResponse{
			Error: "validation_error",
			Code:  "400",
			Details: map[string]any{
				"cursor": "invalid",
			},
		}, http.StatusBadRequest)
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit, ok := parseLimit(limitStr)
	if !ok {
		writeErrorResponse(w, ErrorResponse{
			Error: "validation_error",
			Code:  "400",
			Details: map[string]any{
				"limit": "invalid: must be 1..100",
			},
		}, http.StatusBadRequest)
		return
	}

	items, next, ok := store.GetSummaries(ctx, userID, cursorStr, limit)
	if !ok {
		writeErrorResponse(w, ErrorResponse{
			Error: "not_found",
			Code:  "404",
			Details: map[string]any{
				"userID": "not found",
			},
		}, http.StatusNotFound)
		return
	}

	pg := SummaryPage{
		Items:      items,
		NextCursor: next,
	}

	writeJSONResponse(w, pg, http.StatusOK)
}

// Stub implementation (real logic TBD)
func validateCursor(cursorStr string) bool { return true }

func parseLimit(limitStr string) (int, bool) {
	limit := 20
	ok := true
	if limitStr != "" {
		parsed, err := strconv.Atoi(limitStr)
		if err != nil {
			return 0, false
		}
		limit = parsed
		ok = true
	}
	if limit < 1 || limit > 100 {
		return 0, false
	}
	return limit, ok
}
