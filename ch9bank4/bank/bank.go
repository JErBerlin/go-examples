package bank

import "sync"

var (
	mu      sync.RWMutex // guards balance but allows concurrent reads
	balance int
)

func Deposit(amount int) {
	mu.Lock()
	balance = balance + amount
	mu.Unlock()
}

func Balance() int {
	mu.RLock()
	b := balance
	mu.RUnlock()

	return b
}
