package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	port = ":8080"
)

// persistency and exchange types

// conStore is an in-memory concurrency-safe store guarded by an RWmutex
type conStore struct {
	MuTransactions sync.RWMutex
	Transactions   map[string]Transaction
}

func NewConStore() *conStore {
	store := &conStore{
		MuTransactions: sync.RWMutex{},
		Transactions:   make(map[string]Transaction),
	}

	return store
}

type idemRecord struct {
	Hash       string
	Tr         Transaction
	StatusCode int
}

// conStoreWithCache is an in-memory concurrency-safe store guarded by an RWmutex
// with a mechanism to use idempotency keys
type conStoreWithIdempotency struct {
	*conStore
	idemCache map[string]idemRecord
}

func NewConStoreWithIdempotency() *conStoreWithIdempotency {
	storeWithMutex := &conStore{
		MuTransactions: sync.RWMutex{},
		Transactions:   make(map[string]Transaction),
	}

	store := &conStoreWithIdempotency{
		storeWithMutex,
		make(map[string]idemRecord),
	}

	return store
}

// Main program

func main() {
	mux := setupAndRouting()

	log.Println("listening on " + port)
	if err := http.ListenAndServe(port, mux); err != nil {
		log.Fatal(err)
	}

}

// Models

type TransactionStatus int

const (
	StatusPending TransactionStatus = iota
	StatusCompleted
	StatusFailed
)

var statusName = map[TransactionStatus]string{
	StatusPending:   "pending",
	StatusCompleted: "completed",
	StatusFailed:    "failed",
}

func (ts TransactionStatus) String() string {
	return statusName[ts]
}

func (ts TransactionStatus) MarshalJSON() ([]byte, error) {
	s, ok := statusName[ts]
	if !ok {
		return nil, fmt.Errorf("invalid status: %d", ts)
	}
	return json.Marshal(s)
}

func (ts *TransactionStatus) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("status must be a string: %w", err)
	}

	switch s {
	case "pending":
		*ts = StatusPending
	case "completed":
		*ts = StatusCompleted
	case "failed":
		*ts = StatusFailed
	default:
		return fmt.Errorf("invalid status: %q", s)
	}

	return nil
}

type Transaction struct {
	ID            string            `json:"id"`
	FromAccountID string            `json:"from_account_id"`
	ToAccountID   string            `json:"to_account_id"`
	Amount        float64           `json:"amount"`
	At            time.Time         `json:"at"` // RFC3339 by default
	Status        TransactionStatus `json:"status"`
}

// helper functions

func newID() string {
	var b [16]byte
	_, err := rand.Read(b[:])

	if err != nil { // backup method
		return hex.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	}

	return hex.EncodeToString(b[:])
}

func bindJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	return dec.Decode(dst)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// Routing and Handlers
// setupAndRouting sets up the in-memory store and the server, and register the routes.
func setupAndRouting() *http.ServeMux {
	// setup in-memory, concurrency safe store
	store := NewConStoreWithIdempotency()
	// setup server
	mux := http.NewServeMux()

	// Handlers
	// The store is injected into the handlers that need it.

	// transactions
	mux.HandleFunc("POST /transactions", func(w http.ResponseWriter, r *http.Request) {
		createTransaction(w, r, store)
	})
	mux.HandleFunc("GET /transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		getTransaction(w, r, store)
	})

	return mux
}

// transactions

type transactionRequest struct {
	FromAccountID string  `json:"from_account_id"`
	ToAccountID   string  `json:"to_account_id"`
	Amount        float64 `json:"amount"`
}

func createTransaction(w http.ResponseWriter, r *http.Request, store *conStoreWithIdempotency) {
	key := r.Header.Get("Idempotency-Key") // idempotency-key is optional
	var in transactionRequest

	if err := bindJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}

	err := validateTransactionRequest(in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	fp, err := fingerprint(in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not calculate fingerprint of transaction request")
		return
	}

	// lookup in cache when idempotency key is present
	status := http.StatusAccepted
	if key != "" {
		store.MuTransactions.Lock()
		defer store.MuTransactions.Unlock()
		if rec, ok := store.idemCache[key]; ok {
			// error conflict if the new payload different from cached payload
			if rec.Hash != fp {
				writeError(w, http.StatusConflict, "idempotency key reuse with different payload")
				return
			}
			// else return the original result exactly
			w.Header().Set("Location", "/transactions/"+rec.Tr.ID)
			writeJSON(w, rec.StatusCode, rec.Tr)
			return
		}

		// cache miss: create transaction and store it while lock
		t := Transaction{
			ID:            newID(),
			FromAccountID: in.FromAccountID,
			ToAccountID:   in.ToAccountID,
			Amount:        in.Amount,
			At:            time.Now().UTC(),
			Status:        StatusPending,
		}
		store.Transactions[t.ID] = t
		store.idemCache[key] = idemRecord{Hash: fp, Tr: t, StatusCode: status}

		w.Header().Set("Location", "/transactions/"+t.ID)
		writeJSON(w, status, t)
		return
	}

	// no key, no cache path
	t := Transaction{
		ID:            newID(),
		FromAccountID: in.FromAccountID,
		ToAccountID:   in.ToAccountID,
		Amount:        in.Amount,
		At:            time.Now().UTC(),
		Status:        StatusPending,
	}
	store.MuTransactions.Lock()
	store.Transactions[t.ID] = t
	store.MuTransactions.Unlock()

	w.Header().Set("Location", "/transactions/"+t.ID)
	writeJSON(w, status, t)
}

func getTransaction(w http.ResponseWriter, r *http.Request, store *conStoreWithIdempotency) {
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}

	store.MuTransactions.RLock()
	t, ok := store.Transactions[id]
	store.MuTransactions.RUnlock()

	if !ok {
		writeError(w, http.StatusNotFound, "this payment does not exist")
		return
	}

	writeJSON(w, http.StatusOK, t)
	return
}

// Logic

func validateTransactionRequest(req transactionRequest) error {
	var invalids []string

	if strings.TrimSpace(req.FromAccountID) == "" {
		invalids = append(invalids, "from_account_id")
	}
	if strings.TrimSpace(req.ToAccountID) == "" || strings.TrimSpace(req.FromAccountID) == strings.TrimSpace(req.ToAccountID) {
		invalids = append(invalids, "to_account_id")
	}
	if req.Amount <= 0 || math.IsNaN(req.Amount) || math.IsInf(req.Amount, 0) {
		invalids = append(invalids, "amount")
	}

	if len(invalids) > 0 {
		return fmt.Errorf("invalid or missing: %s", strings.Join(invalids, ", "))
	}

	return nil
}

// helper functions

// loadByKey returns a previously stored transaction by idempotency key
func loadByKey(key string, store *conStoreWithIdempotency) (idemRecord, bool) {
	store.MuTransactions.RLock()
	rec, ok := store.idemCache[key]
	store.MuTransactions.RUnlock()
	return rec, ok
}

// storeByKey stores a transaction in the cache by idempotency key
func storeByKey(key string, rec idemRecord, store *conStoreWithIdempotency) {
	store.MuTransactions.Lock()
	store.idemCache[key] = rec
	store.MuTransactions.Unlock()
}

func fingerprint(req transactionRequest) (string, error) {
	b, err := json.Marshal(req) // field order must be stable (this is assured in Go)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:]), nil
}
