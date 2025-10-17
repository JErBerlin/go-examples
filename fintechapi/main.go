package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// conStore is an in-memory concurrency safe tore guarded by an RWmutex
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

type Transaction struct {
	ID              string
	From_account_id string
	To_account_id   string
	Amount          float64
	At              time.Time
	Status          TransactionStatus
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
	store := NewConStore()
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

func createTransaction(w http.ResponseWriter, r *http.Request, store *conStore) {
	var in struct {
		FromAccountID string  `json:"from_account_id"`
		ToAccountID   string  `json:"to_account_id"`
		Amount        float64 `json:"amount"`
	}

	if err := bindJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}

	if in.Amount <= 0 {
		writeError(w, http.StatusBadRequest, "amount must be positive")
		return
	}

	t, err := processNewTransaction(in.FromAccountID, in.ToAccountID, in.Amount, store)

	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaction could not be accepted for processing")
		return
	}

	w.Header().Set("Location", "/transactions/"+t.ID)
	writeJSON(w, http.StatusAccepted, t)
	return
}

func getTransaction(w http.ResponseWriter, r *http.Request, store *conStore) {
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

func processNewTransaction(fromAccountID, toAccountID string, amount float64, store *conStore) (Transaction, error) {
	id := newID()

	t := Transaction{
		ID:              id,
		From_account_id: from_account_id,
		To_account_id:   to_account_id,
		Amount:          amount,
		At:              time.Now().UTC(),
		Status:          StatusPending,
	}

	store.MuTransactions.Lock()
	store.Transactions[id] = t
	store.MuTransactions.Unlock()

	return t, nil
}
