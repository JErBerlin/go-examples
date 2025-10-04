/*

Package main implements a minimal stdlib-only HTTP API server for Widgets and Payments.

The server provides:
  - POST /widgets        : create a widget (non-idempotent)
  - GET  /widgets        : list widgets
  - GET  /widgets/{id}   : fetch a widget by ID
  - POST /payments       : create a payment (idempotent via Idempotency-Key header)
  - GET  /payments/{id}  : fetch a payment by ID

Storage is in-memory using maps guarded by mutexes. This keeps the
program self-contained and easy to run.

An alternative would be to use an in-memory database such as SQLite (:memory:).
That approach would enable SQL querying, constraints, and transactions, but it
requires an external driver outside the Go standard library.

Idempotency cache has no TTL (entries live for the process lifetime). This is the
simplest implementation; repeated requests are stable forever while the process runs.
The problem is that the memory grows with distinct keys and outdated entries are never evicted.

*/

package main

import (
	"context"
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

var (
	// In-memory stores guarded by mutexes (stdlib-only).
	muWidgets  sync.RWMutex
	muPayments sync.RWMutex
	muIdem     sync.RWMutex

	widgets   = make(map[string]Widget)
	payments  = make(map[string]Payment)
	idemCache = make(map[string]Payment) // Idempotency-Key -> Payment
)

// main intialise the routing and starts the HTTP server on :8080.
func main() {
	mux := registerRoutes()

	log.Println("listening on :8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		log.Fatal(err)
	}
}

// Models

// Widget is a simple resource with a generated ID and a name.
type Widget struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Payment represents a processed payment.
// Currency is one of EUR, USD, GBP. Method is "card" or "bank".
type Payment struct {
	ID       string `json:"id"`
	Amount   int    `json:"amount"`
	Currency string `json:"currency"` // "EUR","USD","GBP"
	Method   string `json:"method"`   // "card","bank"
	Status   string `json:"status"`   // e.g., "processed"
}

// Helpers

// newID generates a random 16-byte hex ID.
// If crypto/rand fails (unlikely), it falls back to a time-based ID.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	// backup in case of error
	return hex.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
}

func bindJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	return dec.Decode(dst)
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error payload with the provided HTTP status.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// validateCurrency returns true if the currency is one of EUR, USD, or GBP.
func validateCurrency(c string) bool {
	switch strings.ToUpper(strings.TrimSpace(c)) {
	case "EUR", "USD", "GBP":
		return true
	default:
		return false
	}
}

// Handlers

// Widgets endpoint

// createWidget creates a new widget resource.
// It is a not idempotent post resource method and it returns the location and body of the new created resource.
func createWidget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := bindJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	// we generate a new UUID for every posted resource (not idempotent)
	id := newID()
	widget := Widget{ID: id, Name: in.Name}

	// persist in memory, needs locking
	muWidgets.Lock()
	widgets[id] = widget
	muWidgets.Unlock()

	w.Header().Set("Location", "/widgets/"+id)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(widget) // ignoring possible encode error
}

// listWidgets returns all widgets currently held in memory.
func listWidgets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	muWidgets.RLock()
	// make a copy of the widgets to be serialised, this minimise time of locking
	out := make([]Widget, 0, len(widgets))
	for _, v := range widgets {
		out = append(out, v)
	}
	muWidgets.RUnlock()

	writeJSON(w, http.StatusOK, out)
}

// getWidget fetches a single widget by its ID.
func getWidget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/widgets/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
	}

	muWidgets.RLock()
	wg, ok := widgets[id]
	muWidgets.RUnlock()

	if !ok {
		writeError(w, http.StatusNotFound, "widget does not exist")
		return
	}

	writeJSON(w, http.StatusOK, wg)
}

// Payments endpoint

// createPayment produces a new payment ensuring idempotency.
// An idempotency key must be provided in the request to look up in cached results.
func createPayment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing idempotency key")
		return
	}

	// look up existing result in cache by idempotency key
	if cached, ok := loadByKey(key); ok {
		// return the original outcome consistently
		w.Header().Set("Location", "/payments/"+cached.ID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(cached)
		return
	}

	// otherwise process and store result tied to the key
	var in struct {
		Amount   int    `json:"amount"`
		Currency string `json:"currency"`
		Method   string `json:"method:`
	}
	if err := bindJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if in.Amount <= 0 {
		writeError(w, http.StatusBadRequest, "amount must be positive")
		return
	}
	if !validateCurrency(in.Currency) {
		writeError(w, http.StatusBadRequest, "this currency is not allowed")
		return
	}
	if strings.TrimSpace(in.Method) == "" {
		writeError(w, http.StatusBadRequest, "this payment method is ot allowed")
		return
	}

	p := processPayment(r.Context(), in.Amount, strings.ToUpper(in.Currency), strings.ToLower(in.Method))
	storeByKey(key, p)

	w.Header().Set("Location", "/payments/"+p.ID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(p) // ignoring possible encode error
}

// getPayment fetches a payment by ID.
func getPayment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/payments/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}

	muPayments.RLock()
	p, ok := payments[id]
	muPayments.RUnlock()

	if !ok {
		writeError(w, http.StatusNotFound, "this payment does not exist")
		return
	}

	writeJSON(w, http.StatusOK, p)

}

// processPayment simulates processing and persists the Payment in memory.
// In a real system, this would perform actual processing and handle failures.
func processPayment(ctx context.Context, amount int, currency, method string) Payment {
	id := newID()
	p := Payment{
		ID:       id,
		Amount:   amount,
		Currency: currency,
		Method:   method,
		Status:   "processed",
	}

	muPayments.Lock()
	payments[id] = p
	muPayments.Unlock()

	return p
}

// loadByKey returns a previously stored Payment by idempotency key.
func loadByKey(key string) (Payment, bool) {
	muIdem.RLock()
	p, ok := idemCache[key]
	muIdem.RUnlock()
	return p, ok
}

// storeByKey stores a Payment under an idempotency key.
func storeByKey(key string, p Payment) {
	muIdem.Lock()
	idemCache[key] = p
	muIdem.Unlock()
}

// HTTP Server

// registerRoutes configures http routes
func registerRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	// Widgets
	mux.HandleFunc("POST /widgets", createWidget)
	mux.HandleFunc("GET /widgets", listWidgets)
	mux.HandleFunc("GET /widgets/{id}", getWidget)

	// Payments
	http.HandleFunc("POST /payments", createPayment)
	http.HandleFunc("GET /payments/", getPayment)

	return mux
}
