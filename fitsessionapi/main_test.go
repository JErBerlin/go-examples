package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T, summaryStore *SummaryStore) (*httptest.Server, func()) {
	t.Helper()

	store := NewSessionStore()
	mux := http.NewServeMux()

	mux.HandleFunc("POST /sessions", func(w http.ResponseWriter, r *http.Request) {
		startSession(store, w, r)
	})

	mux.HandleFunc("GET /users/{userID}/summaries", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/users/") && strings.HasSuffix(r.URL.Path, "/summaries") {
			getSummaries(summaryStore, w, r)
			return
		}
		http.NotFound(w, r)
	})

	ts := httptest.NewServer(mux)

	return ts, func() { ts.Close() }
}

func TestCreateSession(t *testing.T) {
	ts, close := newTestServer(t, nil)
	defer close()

	tests := []struct {
		name       string
		method     string
		body       any
		wantStatus int
		wantError  string
		wantCode   string
		wantKeys   []string // keys expected in .details, e.g. ["userID"], or ["message"] for unknown fields
	}{
		{
			name:       "unknown field",
			method:     http.MethodPost,
			body:       map[string]any{"userID": "u_1", "machineID": "m_1", "extra": true},
			wantStatus: 400,
			wantError:  "validation_error",
			wantCode:   "400",
			wantKeys:   []string{"message"},
		},
		{
			name:       "missing fields",
			method:     http.MethodPost,
			body:       map[string]any{},
			wantStatus: 400,
			wantError:  "validation_error",
			wantCode:   "400",
			wantKeys:   []string{"userID", "machineID"},
		},
		{
			name:       "empty userID",
			method:     http.MethodPost,
			body:       map[string]any{"userID": "", "machineID": "m_1"},
			wantStatus: 400,
			wantError:  "validation_error",
			wantCode:   "400",
			wantKeys:   []string{"userID"},
		},
		{
			name:       "wrong method",
			method:     http.MethodGet,
			body:       nil,
			wantStatus: 405,
			wantError:  "", // mux handles this, no body required
			wantCode:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodyBytes []byte
			if tt.body != nil {
				var err error
				bodyBytes, err = json.Marshal(tt.body)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
			}
			req, err := http.NewRequest(tt.method, ts.URL+"/sessions", bytes.NewReader(bodyBytes))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json; charset=utf-8")

			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status: got %d, want %d", resp.StatusCode, tt.wantStatus)
			}

			if tt.wantStatus == 405 {
				// No body expected for mux 405 and content-type not relevant
				return
			}

			ct := resp.Header.Get("Content-Type")
			if tt.wantStatus != 405 && !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type: got %q", ct)
			}

			var errResp ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if errResp.Error != tt.wantError {
				t.Errorf("error: got %q, want %q", errResp.Error, tt.wantError)
			}
			if errResp.Code != tt.wantCode {
				t.Errorf("code: got %q, want %q", errResp.Code, tt.wantCode)
			}
			for _, key := range tt.wantKeys {
				if _, ok := errResp.Details[key]; !ok {
					t.Errorf("details: missing key %q", key)
				}
			}
		})
	}
}

func TestGetSummaries(t *testing.T) {
	summaryStore := &SummaryStore{
		m: map[string]SummaryItem{
			"u_123": {
				Date:          "2025-10-07",
				TotalWorkouts: 2,
				TotalReps:     150,
				TotalWeightKg: 4800.0,
				AvgHR:         ptr(122),
			},
		},
	}

	ts, cleanup := newTestServer(t, summaryStore)
	defer cleanup()

	t.Run("valid request", func(t *testing.T) {
		resp, err := ts.Client().Get(ts.URL + "/users/u_123/summaries")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}

		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "application/json") {
			t.Errorf("missing or wrong content-type: got %q", ct)
		}

		var page SummaryPage
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			t.Fatalf("failed to decode summary page: %v", err)
		}
		if len(page.Items) != 1 {
			t.Errorf("expected 1 item, got %d", len(page.Items))
		}
		if page.Items[0].Date == "" {
			t.Error("expected non-empty date")
		}
	})

	t.Run("invalid limit", func(t *testing.T) {
		resp, err := ts.Client().Get(ts.URL + "/users/u_123/summaries?limit=200")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}

		var errResp ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error: %v", err)
		}
		if errResp.Error != "validation_error" {
			t.Errorf("wrong error: got %q", errResp.Error)
		}
		if _, ok := errResp.Details["limit"]; !ok {
			t.Error("expected details to include 'limit'")
		}
	})

	t.Run("user not found", func(t *testing.T) {
		resp, err := ts.Client().Get(ts.URL + "/users/unknown_user/summaries")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}

		var errResp ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error: %v", err)
		}
		if errResp.Error != "not_found" {
			t.Errorf("wrong error: got %q", errResp.Error)
		}
	})
}
