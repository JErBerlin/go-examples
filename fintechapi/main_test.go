package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestServer(t *testing.T) (*httptest.Server, *conStoreWithIdempotency) {
	t.Helper()

	store := NewConStoreWithIdempotency()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /transactions", func(w http.ResponseWriter, r *http.Request) {
		createTransaction(w, r, store)
	})
	mux.HandleFunc("GET /transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		getTransaction(w, r, store)
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	return ts, store

}

func postJSON(t *testing.T, url string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()

	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))

	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s failed: %v", url, err)
	}
	defer res.Body.Close()

	data := new(bytes.Buffer)
	_, _ = data.ReadFrom(res.Body)

	return res, data.Bytes()
}

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()

	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s failed: %v", url, err)
	}
	defer res.Body.Close()

	data := new(bytes.Buffer)
	_, _ = data.ReadFrom(res.Body)

	return res, data.Bytes()
}

func TestValidateTransactionRequest(t *testing.T) {
	tests := []struct {
		Name          string
		WantError     bool
		FromAccountID string
		ToAccountID   string
		Amount        float64
	}{
		{
			Name:          "correct transaction request",
			WantError:     false,
			FromAccountID: "ac123",
			ToAccountID:   "ac125",
			Amount:        100.00,
		},
		{
			Name:          "negative amount",
			WantError:     true,
			FromAccountID: "ac234",
			ToAccountID:   "ac134",
			Amount:        -100.00,
		},
		{
			Name:          "same account in to and from",
			WantError:     true,
			FromAccountID: "ac123",
			ToAccountID:   "ac123",
			Amount:        100.00,
		},
	}

	for _, test := range tests {

		req := transactionRequest{
			FromAccountID: test.FromAccountID,
			ToAccountID:   test.ToAccountID,
			Amount:        test.Amount,
		}

		err := validateTransactionRequest(req)

		if err != nil {
			if test.WantError == false {
				t.Errorf("%s: want no error, got %s", test.Name, err)
			}
		} else if test.WantError == true {
			t.Errorf("%s: want error, got no error", test.Name)
		}
	}

	return
}

func TestCreateAndGetTransaction(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	// Test data
	in := map[string]any{
		"from_account_id": "ac123",
		"to_account_id":   "ac125",
		"amount":          100.00,
	}

	// POST
	res, body := postJSON(t, ts.URL+"/transactions", in, nil)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", res.StatusCode, string(body))
	}
	loc := res.Header.Get("Location")
	if !strings.HasPrefix(loc, "/transactions/") {
		t.Fatalf("missing/invalid Location: %q", loc)
	}

	// GET using Location from POST req
	res2, body2 := get(t, ts.URL+loc)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", res2.StatusCode, string(body2))
	}
	// status should be string "pending"
	if !strings.Contains(string(body2), `"status":"pending"`) {
		t.Errorf("expected status string in body, got %s", string(body2))
	}
}

func TestIdempotency_SameKeySamePayload_ReturnsSameResponse(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	key := "k123"
	headers := map[string]string{"Idempotency-Key": key}
	in := map[string]any{"from_account_id": "A1", "to_account_id": "A2", "amount": 10.0}

	res1, body1 := postJSON(t, ts.URL+"/transactions", in, headers)
	loc1 := res1.Header.Get("Location")
	if res1.StatusCode != http.StatusAccepted || loc1 == "" {
		t.Errorf("first call expected 202 with Location, got %d", res1.StatusCode)
	}
	res2, body2 := postJSON(t, ts.URL+"/transactions", in, headers)
	loc2 := res2.Header.Get("Location")

	if res2.StatusCode != res1.StatusCode || loc2 != loc1 || string(body1) != string(body2) {
		t.Errorf("idempotent replay mismatch:\nstatus %d vs %d\nloc %q vs %q\nbody %s vs %s",
			res1.StatusCode, res2.StatusCode, loc1, loc2, string(body1), string(body2))
	}
}

func TestIdempotency_SameKeyDifferentPayload_409(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	key := "k123"
	headers := map[string]string{"Idempotency-Key": key}

	in1 := map[string]any{"from_account_id": "A1", "to_account_id": "A2", "amount": 10.0}
	in2 := map[string]any{"from_account_id": "A1", "to_account_id": "A2", "amount": 20.0}

	res1, _ := postJSON(t, ts.URL+"/transactions", in1, headers)
	if res1.StatusCode != http.StatusAccepted {
		t.Errorf("first call expected 202, got %d", res1.StatusCode)
	}
	res2, _ := postJSON(t, ts.URL+"/transactions", in2, headers)
	if res2.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 on different payload, got %d", res2.StatusCode)
	}
}

func TestNoKey_NotCached_ProducesDifferentIDs(t *testing.T) {
	ts, _ := newTestServer(t)
	defer ts.Close()

	in := map[string]any{"from_account_id": "A1", "to_account_id": "A2", "amount": 10.0}
	res1, _ := postJSON(t, ts.URL+"/transactions", in, nil)
	res2, _ := postJSON(t, ts.URL+"/transactions", in, nil)
	if res1.Header.Get("Location") == "" || res2.Header.Get("Location") == "" {
		t.Fatalf("missing Location headers")
	}
	if res1.Header.Get("Location") == res2.Header.Get("Location") {
		t.Errorf("requests without key should not be cached; got same Location %q", res1.Header.Get("Location"))
	}
}

func TestConcurrentSameKey_SerializesToSingleTransaction(t *testing.T) {
	ts, store := newTestServer(t)
	defer ts.Close()

	key := "concurrent-key"
	headers := map[string]string{"Idempotency-Key": key}
	in := map[string]any{"from_account_id": "A1", "to_account_id": "A2", "amount": 10.0}

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)

	statuses := make([]int, N)
	locations := make([]string, N)

	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			res, _ := postJSON(t, ts.URL+"/transactions", in, headers)
			statuses[i] = res.StatusCode
			locations[i] = res.Header.Get("Location")
		}(i)
	}
	wg.Wait()

	// All should agree on location
	loc0 := locations[0]
	for i, loc := range locations {
		if loc != loc0 {
			t.Errorf("location mismatch at request %d: %q vs %q", i, loc, loc0)
		}
	}

	// Exactly one transaction record created for that ID
	id := strings.TrimPrefix(loc0, "/transactions/")
	time.Sleep(50 * time.Millisecond) // tinyh delay to let writes finish
	store.MuTransactions.RLock()
	_, ok := store.Transactions[id]
	store.MuTransactions.RUnlock()
	if !ok {
		t.Fatalf("expected transaction %s to exist", id)
	}
}
