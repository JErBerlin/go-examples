package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
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

// conStoreWithCache is an in-memory concurrency-safe store guarded by an RWmutex
// with a mechanism to use idempotency keys
type conStoreWithIdempotency struct {
	*conStore
	idemCache map[string]Transaction
}

func NewConStoreWithIdempotency() *conStoreWithIdempotency {
	storeWithMutex := &conStore{
		MuTransactions: sync.RWMutex{},
		Transactions:   make(map[string]Transaction),
	}

	store := &conStoreWithIdempotency{
		storeWithMutex,
		make(map[string]Transaction),
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
		*ts = 0
	case "completed":
		*ts = 1
	case "failed":
		*ts = 2
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

	// lookup in cache in case
	if key != "" {
		cached, ok := loadByKey(key, store)
		if ok {
			// error conflict if the new payload diffferent from cached payload
			if in.Amount != cached.Amount ||
				in.FromAccountID != cached.FromAccountID ||
				in.ToAccountID != cached.ToAccountID {
				writeError(w, http.StatusConflict, "idempotency key reuse with different payload")
				return
			}

			// else return the original result
			w.Header().Set("Location", "/transactions/"+cached.ID)
			writeJSON(w, http.StatusAccepted, cached)
			return
		}
	}

	t, err := processNewTransaction(in.FromAccountID, in.ToAccountID, in.Amount, store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaction could not be accepted for processing")
		return
	}
	storeByKey(key, t, store)

	w.Header().Set("Location", "/transactions/"+t.ID)
	writeJSON(w, http.StatusAccepted, t)
	return
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

func processNewTransaction(fromAccountID, toAccountID string, amount float64, store *conStoreWithIdempotency) (Transaction, error) {
	id := newID()

	t := Transaction{
		ID:            id,
		FromAccountID: fromAccountID,
		ToAccountID:   toAccountID,
		Amount:        amount,
		At:            time.Now().UTC(),
		Status:        StatusPending,
	}

	store.MuTransactions.Lock()
	store.Transactions[id] = t
	store.MuTransactions.Unlock()

	return t, nil
}

func validateTransactionRequest(req transactionRequest) error {
	var invalids []string

	if strings.TrimSpace(req.FromAccountID) == "" {
		invalids = append(invalids, "from_account_id")
	}
	if strings.TrimSpace(req.ToAccountID) == "" {
		invalids = append(invalids, "to_account_id")
	}
	if req.Amount <= 0 {
		invalids = append(invalids, "amount")
	}

	if len(invalids) > 0 {
		return fmt.Errorf("invalid or missing: %s", strings.Join(invalids, ", "))
	}
	return nil

}

// helper functions

// loadByKey returns a previously stored transaction by idempotency key
func loadByKey(key string, store *conStoreWithIdempotency) (Transaction, bool) {
	store.MuTransactions.RLock()
	t, ok := store.idemCache[key]
	store.MuTransactions.RUnlock()
	return t, ok
}

// storeByKey stores a transaction in the cache by idempotency key
func storeByKey(key string, t Transaction, store *conStoreWithIdempotency) {
	store.MuTransactions.Lock()
	store.idemCache[key] = t
	store.MuTransactions.Unlock()
}
