package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	mux.HandleFunc("GET /transactions", func(w http.ResponseWriter, r *http.Request) {
		listTransactions(w, r, store)
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

func TestAPI(t *testing.T) {
	t.Run("CreateAndGetTransaction", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("Idempotency_SameKeySamePayload_ReturnsSameResponse", func(t *testing.T) {
		t.Parallel()
		ts, _ := newTestServer(t)
		defer ts.Close()

		key := "k123"
		headers := map[string]string{"Idempotency-Key": key}
		in := map[string]any{"from_account_id": "A1", "to_account_id": "A2", "amount": 10.0}

		res1, body1 := postJSON(t, ts.URL+"/transactions", in, headers)
		loc1 := res1.Header.Get("Location")
		if res1.StatusCode != http.StatusAccepted || loc1 == "" {
			t.Errorf("first call expected 202 with Location, got %d and location \"%s\"", res1.StatusCode, loc1)
		}
		res2, body2 := postJSON(t, ts.URL+"/transactions", in, headers)
		loc2 := res2.Header.Get("Location")

		if res2.StatusCode != res1.StatusCode || loc2 != loc1 || string(body1) != string(body2) {
			t.Errorf("idempotent replay mismatch:\nstatus %d vs %d\nloc %q vs %q\nbody %s vs %s",
				res1.StatusCode, res2.StatusCode, loc1, loc2, string(body1), string(body2))
		}
	})

	t.Run("Idempotency_SameKeyDifferentPayload_409", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("NoKey_NotCached_ProducesDifferentIDs", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("ConcurrentSameKey_SerializesToSingleTransaction", func(t *testing.T) {
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
	})
	t.Run("StrictJSONUnknownField_400", func(t *testing.T) {
		t.Parallel()
		ts, _ := newTestServer(t)
		defer ts.Close()

		in := map[string]any{
			"from_account_id": "A1",
			"to_account_id":   "A2",
			"amount":          1.0,
			"extra":           "nope", // unknown
		}
		res, body := postJSON(t, ts.URL+"/transactions", in, nil)
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d body=%s", res.StatusCode, string(body))
		}
	})

	t.Run("ResponseHeaders_ContentTypeAndLocation", func(t *testing.T) {
		t.Parallel()
		ts, _ := newTestServer(t)
		defer ts.Close()

		in := map[string]any{
			"from_account_id": "A1",
			"to_account_id":   "A2",
			"amount":          10.0,
		}
		res, body := postJSON(t, ts.URL+"/transactions", in, nil)
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("expected 202, got %d body=%s", res.StatusCode, string(body))
		}
		if ct := res.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("expected Content-Type application/json, got %q", ct)
		}
		loc := res.Header.Get("Location")
		if !strings.HasPrefix(loc, "/transactions/") || len(loc) <= len("/transactions/") {
			t.Fatalf("invalid Location: %q", loc)
		}
	})

}

func BenchmarkCreate_NoKey(b *testing.B) {
	t := &testing.T{}
	ts, _ := newTestServer(t)
	defer ts.Close()

	in := map[string]any{"from_account_id": "A1", "to_account_id": "A2", "amount": 10.0}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		res, _ := postJSON(t, ts.URL+"/transactions", in, nil)
		res.Body.Close()
	}

}

func BenchmarkCreate_Idempotent_SameKey(b *testing.B) {
	t := &testing.T{}
	ts, _ := newTestServer(t)
	defer ts.Close()

	key := "bench-samekey"
	h := map[string]string{"Idempotency-Key": key}
	in := map[string]any{"from_account_id": "A1", "to_account_id": "A2", "amount": 10.0}

	// Warm-up first call to populate cache
	res, _ := postJSON(t, ts.URL+"/transactions", in, h)
	res.Body.Close()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		res, _ := postJSON(t, ts.URL+"/transactions", in, h)
		res.Body.Close()
	}
}

func BenchmarkCreate_Idempotent_DifferentKeys_Parallel(b *testing.B) {
	ts, _ := newTestServer(&testing.T{})
	defer ts.Close()

	var ctr uint64
	in := map[string]any{"from_account_id": "A1", "to_account_id": "A2", "amount": 10.0}

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// unique key per iteration to avoid serialization
			key := "bench-key-" + strconv.FormatUint(atomic.AddUint64(&ctr, 1), 10)
			h := map[string]string{"Idempotency-Key": key}
			res, _ := postJSON(&testing.T{}, ts.URL+"/transactions", in, h)
			res.Body.Close()
		}
	})
}

func BenchmarkCreate_NoKey_Parallel(b *testing.B) {
	ts, _ := newTestServer(&testing.T{})
	defer ts.Close()

	in := map[string]any{"from_account_id": "A1", "to_account_id": "A2", "amount": 10.0}

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			res, _ := postJSON(&testing.T{}, ts.URL+"/transactions", in, nil)
			res.Body.Close()
		}
	})
}

func TestIdempotency_TTLEviction(t *testing.T) {
	// Build a custom server: same handlers, but our own sweeper with tiny TTL.
	store := NewConStoreWithIdempotency()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /transactions", func(w http.ResponseWriter, r *http.Request) {
		createTransaction(w, r, store)
	})
	mux.HandleFunc("GET /transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		getTransaction(w, r, store)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Start a short-lived sweeper
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ttl := 50 * time.Millisecond
	interval := 20 * time.Millisecond
	go startCacheSweeperWith(ctx, store, ttl, interval)

	// Create via idempotency key
	key := "ttl-key"
	headers := map[string]string{"Idempotency-Key": key}
	in := map[string]any{"from_account_id": "A1", "to_account_id": "A2", "amount": 10.0}
	res, _ := postJSON(t, ts.URL+"/transactions", in, headers)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", res.StatusCode)
	}

	// Wait > TTL and sweep interval
	time.Sleep(150 * time.Millisecond)

	// Reuse same key + same payload — cache should be gone, so this is treated as new
	res2, _ := postJSON(t, ts.URL+"/transactions", in, headers)
	if res2.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 on reinsert after eviction, got %d", res2.StatusCode)
	}
	// assert different Location to confirm a new transaction was created
	if res.Header.Get("Location") == res2.Header.Get("Location") {
		t.Fatalf("expected eviction to force a new transaction (different Location)")
	}
}

func TestListTransactions_FirstPageAndNextCursor(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	defer ts.Close()

	// Create 3 transactions from account A1, 1 from A2
	headers := map[string]string{"Content-Type": "application/json"}
	body := func(fa, ta string, amt float64) map[string]any {
		return map[string]any{"from_account_id": fa, "to_account_id": ta, "amount": amt}
	}
	_, _ = postJSON(t, ts.URL+"/transactions", body("A1", "B1", 10), headers)
	time.Sleep(time.Millisecond) // ensure At time ordering
	_, _ = postJSON(t, ts.URL+"/transactions", body("A1", "B2", 11), headers)
	time.Sleep(time.Millisecond)
	_, _ = postJSON(t, ts.URL+"/transactions", body("A1", "B3", 12), headers)
	_, _ = postJSON(t, ts.URL+"/transactions", body("A2", "B9", 99), headers)

	// First page for A1 with limit=2
	res, b1 := get(t, ts.URL+"/transactions?from_account_id=A1&limit=2")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", res.StatusCode)
	}
	var page1 struct {
		Items      []Transaction `json:"items"`
		NextCursor string        `json:"next_cursor"`
	}
	if err := json.Unmarshal(b1, &page1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(page1.Items) != 2 || page1.NextCursor == "" {
		t.Fatalf("expected 2 items and non-empty cursor, got %d, cursor=%q", len(page1.Items), page1.NextCursor)
	}

	// Second page using cursor
	res2, b2 := get(t, ts.URL+"/transactions?from_account_id=A1&limit=2&cursor="+page1.NextCursor)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", res2.StatusCode)
	}
	var page2 struct {
		Items      []Transaction `json:"items"`
		NextCursor string        `json:"next_cursor"`
	}
	if err := json.Unmarshal(b2, &page2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// No overlap between page1 and page2 IDs
	idset := map[string]struct{}{}
	for _, it := range page1.Items {
		idset[it.ID] = struct{}{}
	}
	for _, it := range page2.Items {
		if _, dup := idset[it.ID]; dup {
			t.Fatalf("overlap between pages on ID %s", it.ID)
		}
	}
	// Since we only had 3 for A1, page2 should have 1 item, and next_cursor should be empty.
	if len(page2.Items) != 1 || page2.NextCursor != "" {
		t.Fatalf("expected final page with 1 item and empty cursor, got %d, cursor=%q", len(page2.Items), page2.NextCursor)
	}
}

func TestListTransactions_CursorQueryMismatch_400(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	defer ts.Close()

	// seed two txns for A1 so first page (limit=1) yields a next_cursor
	_, _ = postJSON(t, ts.URL+"/transactions",
		map[string]any{"from_account_id": "A1", "to_account_id": "B1", "amount": 10.0}, nil)
	time.Sleep(time.Millisecond) // ensure distinct At
	_, _ = postJSON(t, ts.URL+"/transactions",
		map[string]any{"from_account_id": "A1", "to_account_id": "B2", "amount": 11.0}, nil)

	// get first page for A1, get cursor
	res, b1 := get(t, ts.URL+"/transactions?from_account_id=A1&limit=1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d body=%q", res.StatusCode, string(b1))
	}
	var p1 struct {
		Items      []Transaction `json:"items"`
		NextCursor string        `json:"next_cursor"`
	}
	if err := json.Unmarshal(b1, &p1); err != nil {
		t.Fatalf("unmarshal: %v body=%q", err, string(b1))
	}
	if len(p1.Items) != 1 || p1.NextCursor == "" {
		t.Fatalf("expected 1 item and non-empty cursor, got %d, cursor=%q", len(p1.Items), p1.NextCursor)
	}

	// re-use the cursor but change from_account_id → should 400
	res2, _ := get(t, ts.URL+"/transactions?from_account_id=A2&limit=1&cursor="+p1.NextCursor)
	if res2.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 on cursor/query mismatch, got %d", res2.StatusCode)
	}
}

func TestListTransactions_LimitParsing(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	defer ts.Close()
	// seed
	_, _ = postJSON(t, ts.URL+"/transactions", map[string]any{"from_account_id": "A1", "to_account_id": "B1", "amount": 10.0}, nil)

	// invalid limit -> 400
	res, _ := get(t, ts.URL+"/transactions?from_account_id=A1&limit=zero")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for invalid limit, got %d", res.StatusCode)
	}

	// big limit -> clamped to max; we can only assert it's 200 OK and not erroring
	res2, _ := get(t, ts.URL+"/transactions?from_account_id=A1&limit=1000000")
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("want 200 for big limit (clamped), got %d", res2.StatusCode)
	}
}

func TestListTransactions_InsertBetweenPages_NoDuplicates(t *testing.T) {
	ts, store := newTestServer(t)
	defer ts.Close()

	// create two for A1
	_, _ = postJSON(t, ts.URL+"/transactions", map[string]any{"from_account_id": "A1", "to_account_id": "B1", "amount": 10.0}, nil)
	time.Sleep(time.Millisecond)
	_, _ = postJSON(t, ts.URL+"/transactions", map[string]any{"from_account_id": "A1", "to_account_id": "B2", "amount": 11.0}, nil)

	// first page limit=1
	res, b1 := get(t, ts.URL+"/transactions?from_account_id=A1&limit=1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d body=%q", res.StatusCode, string(b1))
	}
	var p1 struct {
		Items      []Transaction `json:"items"`
		NextCursor string        `json:"next_cursor"`
	}
	if err := json.Unmarshal(b1, &p1); err != nil {
		t.Fatalf("unmarshal: %v body=%q", err, string(b1))
	}
	if len(p1.Items) == 0 {
		t.Fatalf("expected at least 1 item on first page")
	}

	// inject a NEWER transaction (after a tiny sleep so At is greater)
	time.Sleep(2 * time.Millisecond)
	_, _ = postJSON(t, ts.URL+"/transactions", map[string]any{"from_account_id": "A1", "to_account_id": "B3", "amount": 12.0}, nil)

	// second page with cursor
	res, b2 := get(t, ts.URL+"/transactions?from_account_id=A1&limit=2&cursor="+p1.NextCursor)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d body=%q", res.StatusCode, string(b1))
	}
	var p2 struct {
		Items      []Transaction `json:"items"`
		NextCursor string        `json:"next_cursor"`
	}
	if err := json.Unmarshal(b2, &p2); err != nil {
		t.Fatalf("unmarshal: %v body=%q", err, string(b1))
	}
	if len(p2.Items) == 0 {
		t.Fatalf("expected at least 1 item on first page")
	}

	// ensure we didn't duplicate the last item of page1
	id0 := p1.Items[0].ID
	for _, it := range p2.Items {
		if it.ID == id0 {
			t.Fatalf("duplicate item across pages: %s", id0)
		}
	}

	// sanity: items belong to A1
	store.MuTransactions.RLock()
	defer store.MuTransactions.RUnlock()
	for _, it := range p2.Items {
		if it.FromAccountID != "A1" {
			t.Fatalf("unexpected account in page2: %s", it.FromAccountID)
		}
	}
}
