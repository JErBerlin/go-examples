package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(t *testing.T) (*httptest.Server, *conStoreWithIdempotency) {
	t.Helper()

	store := NewConStoreWithIdempotency()
	mux, _ := func() (*http.ServeMux, func()) {
		// replicate setupAndRouting without starting network listener
		mux := http.NewServeMux()
		// no sweeper here: use a cancellable context if you prefer to test TTL
		// minimal routes
		mux.HandleFunc("POST /transactions", func(w http.ResponseWriter, r *http.Request) {
			createTransaction(w, r, store)
		})
		mux.HandleFunc("GET /transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
			getTransaction(w, r, store)
		})
		return mux, func() {}
	}()

	return httptest.NewServer(mux), store
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
