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

var (
	// in-memory store (guarded by mutex)
	muTransactions sync.Mutex
	transactions   = make(map[string]Transaction)
)

func main() {
	mux := registerRoutes()

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

func registerRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	// transactions
	mux.HandleFunc("POST /transactions", createTransaction)
	mux.HandleFunc("GET /transactions/{id}", getTransaction)

	return mux
}

func createTransaction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		From_account_id string  `json:"from_account_id"`
		To_account_id   string  `json:"to_account_id"`
		Amount          float64 `json:"amount"`
	}

	if err := bindJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}

	if in.Amount <= 0 {
		writeError(w, http.StatusBadRequest, "amount must be positive")
		return
	}

	t, err := processNewTransaction(in.From_account_id, in.To_account_id, in.Amount)

	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaction could not be accepted for processing")
		return
	}

	w.Header().Set("Location", "/transactions/"+t.ID)
	writeJSON(w, http.StatusAccepted, t)
	return
}

func getTransaction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}

	muTransactions.Lock()
	t, ok := transactions[id]
	muTransactions.Unlock()

	if !ok {
		writeError(w, http.StatusNotFound, "this payment does not exist")
		return
	}

	writeJSON(w, http.StatusOK, t)
	return
}

// Logic

func processNewTransaction(from_account_id, to_account_id string, amount float64) (Transaction, error) {
	id := newID()

	t := Transaction{
		ID:              id,
		From_account_id: from_account_id,
		To_account_id:   to_account_id,
		Amount:          amount,
		At:              time.Now().UTC(),
		Status:          StatusPending,
	}

	muTransactions.Lock()
	transactions[id] = t
	muTransactions.Unlock()

	return t, nil
}
