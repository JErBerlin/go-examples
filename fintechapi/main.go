package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	port                = ":8080"
	defaultEntriesLimit = 20
	maxEntriesLimit     = 100
)

var (
	idemTTL       = 24 * time.Hour
	sweepInterval = 5 * time.Minute
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
	CreatedAt  time.Time
	Body       []byte
	Location   string
}

// conStoreWithCache is an in-memory concurrency-safe store guarded by an RWmutex
// with a mechanism to use idempotency keys
type conStoreWithIdempotency struct {
	*conStore
	idemCache map[string]idemRecord
	keyLocks  *lockRegistry
}

func NewConStoreWithIdempotency() *conStoreWithIdempotency {
	return &conStoreWithIdempotency{
		conStore:  &conStore{Transactions: make(map[string]Transaction)},
		idemCache: make(map[string]idemRecord),
		keyLocks:  newLockRegistry(),
	}
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

type lockRegistry struct {
	mu sync.Mutex
	m  map[string]*keyLock
}

func newLockRegistry() *lockRegistry {
	return &lockRegistry{m: make(map[string]*keyLock)}
}

func (r *lockRegistry) acquire(key string) (unlock func()) {
	r.mu.Lock()
	kl, ok := r.m[key]
	if !ok {
		kl = &keyLock{}
		r.m[key] = kl
	}
	kl.refs++
	r.mu.Unlock()
	kl.mu.Lock() // serialize same-key requests

	return func() {
		kl.mu.Unlock()
		r.mu.Lock()
		kl.refs--
		if kl.refs == 0 {
			delete(r.m, key)
		}
		r.mu.Unlock()
	}
}

func startCacheSweeperWith(ctx context.Context, s *conStoreWithIdempotency, ttl, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			s.MuTransactions.Lock()
			for k, rec := range s.idemCache {
				if now.Sub(rec.CreatedAt) > ttl {
					delete(s.idemCache, k)
				}
			}
			s.MuTransactions.Unlock()
		}
	}
}

// Main program

func main() {
	mux, cancel := setupAndRouting()
	defer cancel()

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
func setupAndRouting() (*http.ServeMux, context.CancelFunc) {
	// setup in-memory, concurrency safe store
	store := NewConStoreWithIdempotency()
	// setup server
	mux := http.NewServeMux()
	// setup cache sweeper
	ctx, cancel := context.WithCancel(context.Background())
	go startCacheSweeperWith(ctx, store, idemTTL, sweepInterval)

	// Handlers
	// The store is injected into the handlers that need it.

	// transactions
	mux.HandleFunc("POST /transactions", func(w http.ResponseWriter, r *http.Request) {
		createTransaction(w, r, store)
	})
	mux.HandleFunc("GET /transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		getTransaction(w, r, store)
	})

	return mux, cancel
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
		// Serialize only same-key requests
		unlockKey := store.keyLocks.acquire(key)
		defer unlockKey()

		// locked idempotency check/insert:
		store.MuTransactions.Lock()
		defer store.MuTransactions.Unlock()

		if rec, ok := store.idemCache[key]; ok {
			if rec.Hash != fp {
				writeError(w, http.StatusConflict, "idempotency key reuse with different payload")
				return
			}
			w.Header().Set("Location", rec.Location)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(rec.StatusCode)
			_, _ = w.Write(rec.Body)
			return
		}

		t := Transaction{
			ID:            newID(),
			FromAccountID: in.FromAccountID,
			ToAccountID:   in.ToAccountID,
			Amount:        in.Amount,
			At:            time.Now().UTC(),
			Status:        StatusPending,
		}
		store.Transactions[t.ID] = t
		body, _ := json.Marshal(t)
		loc := "/transactions/" + t.ID
		store.idemCache[key] = idemRecord{
			Hash:       fp,
			Tr:         t,
			StatusCode: status,
			CreatedAt:  time.Now(),
			Body:       body,
			Location:   loc,
		}

		w.Header().Set("Location", loc)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}

	// No key: normal path (no per-key lock)
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

func listTransactions(w http.ResponseWriter, r *http.Request, store *conStoreWithIdempotency) {
	from := strings.TrimSpace(r.URL.Query().Get("from_account_id"))
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid limit")
		return
	}

	curStr := r.URL.Query().Get("cursor")
	cur, err := decodeCursor(curStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// cursor/query mismatch check
	if cur.FA != "" && cur.FA != from {
		writeError(w, http.StatusBadRequest, "cursor does not match query")
		return
	}

	// Snapshot under rread lock
	store.MuTransactions.RLock()
	items := make([]Transaction, 0, len(store.Transactions))
	for _, t := range store.Transactions {
		if from != "" && t.FromAccountID != from {
			continue
		}
		items = append(items, t)
	}
	store.MusTransactions.RUnlock()

	// Stort by (At ASC, ID ASC)
	sort.Slice(items, func(i, j int) bool {
		if items[i].At.Before(items[j].At) {
			return true
		}
		if items[i].At.After(items[j].At) {
			return false
		}
		return items[i].ID < items[j].ID
	})

	// Apply keyset window
	start := 0
	if cur.At != (time.Time{}) {
		// find first strictly after cursor
		// linear scan is fine for in-mem; swap to binary search if needed
		for idx := range items {
			if afterCursor(items[idx], cur) {
				start = idx
				break
			}
			start = len(items) // if none is after, empty page
		}
	}

	// Page slice
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	page := items[start:end]

	// Build next cursor (only if more items remain)
	next := ""
	if end < len(items) && len(page) > 0 {
		last := page[len(page)-1]
		nc := trCursor{At: last.At.UTC(), ID: last.ID, FA: from}
		if s, err := encodeCursor(nc); err == nil {
			next = s
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       page,
		"next_cursor": next,
	})
}

type trCursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
	FA string    `json:"fa"` // from account
}

func encodeCursor(c txCursor) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeCursor(s string) (txCursor, error) {
	var c txCursor
	if strings.TrimSpace(s) == "" {
		return c, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, fmt.Errorf("invalid cursor encoding")
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("invalid cursor payload")
	}
	if c.At.IsZero() || strings.TrimSpace(c.ID) == "" {
		return c, fmt.Errorf("invalid cursor fields")
	}
	return c, nil
}

func parseLimit(q string) (int, error) {
	if strings.TrimSpace(q) == "" {
		return defaultLimit, nil
	}

	n, err := strconv.Atoi(q)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid limit")
	}
	if n > maxLimit {
		n = maxLimit
	}
	return n, nil
}

func afterCursor(a Transaction, cur trCursor) bool {
	// (At,ID) > (cur.At, cur.ID)
	if a.At.After(cur.At) {
		return true
	}
	if a.At.Equal(cur.At) && a.ID > cur.ID {
		return true
	}
	return false
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
