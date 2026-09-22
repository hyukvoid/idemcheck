// Package main implements the UNSAFE demo order API.
//
// The bug is intentional and easy to read: the handler checks the
// idempotency key, sleeps, inserts the order, then saves the key — with no
// synchronization between check and insert (classic TOCTOU). Concurrent
// requests that share one key all pass the check before any of them saves
// the key, so duplicates are created.
//
// The sleep is deliberately generous (250ms) so the race window is wide
// enough to reproduce reliably, not a one-in-a-thousand timing fluke.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

type order struct {
	OrderID int    `json:"order_id"`
	ItemID  int    `json:"item_id"`
	Qty     int    `json:"qty"`
	Status  string `json:"status"`
}

type store struct {
	mu sync.Mutex
	// orders and keys are guarded individually, but the CHECK-then-INSERT
	// sequence in the handler is not — that is the bug under test.
	nextID int
	orders map[int]order
	keys   map[string]int // idempotency key -> order id
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}
	s := &store{
		nextID: 800,
		orders: map[int]order{},
		keys:   map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", s.handleCreate)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	log.Printf("unsafe-order-api listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

type createReq struct {
	ItemID int `json:"item_id"`
	Qty    int `json:"qty"`
}

func (s *store) handleCreate(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpError(w, http.StatusBadRequest, "missing Idempotency-Key header")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpError(w, http.StatusBadRequest, "unreadable body")
		return
	}
	var req createReq
	if err := json.Unmarshal(body, &req); err != nil {
		httpError(w, http.StatusBadRequest, "body must be JSON {item_id, qty}")
		return
	}

	// BUG (intentional): check key...
	s.mu.Lock()
	existingID, found := s.keys[key]
	s.mu.Unlock()

	if found {
		// Replay the original response verbatim (same status, same body) —
		// this part is correct, which is exactly what makes the demo useful:
		// sequential retries look idempotent, only concurrency breaks it.
		s.writeOrder(w, http.StatusCreated, existingID)
		return
	}

	// ...artificial work window: concurrent duplicates all slip through here.
	time.Sleep(250 * time.Millisecond)

	// ...insert order...
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	o := order{OrderID: id, ItemID: req.ItemID, Qty: req.Qty, Status: "created"}
	s.orders[id] = o
	s.mu.Unlock()

	// ...save key. Too late: every sleeping duplicate already passed the check.
	s.mu.Lock()
	s.keys[key] = id
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(o)
}

func (s *store) writeOrder(w http.ResponseWriter, status, id int) {
	s.mu.Lock()
	o, ok := s.orders[id]
	s.mu.Unlock()
	if !ok {
		httpError(w, http.StatusInternalServerError, "order vanished")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(o)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
