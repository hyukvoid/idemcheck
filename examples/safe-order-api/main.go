// Package main implements the SAFE demo order API.
//
// The fix for the unsafe version's TOCTOU bug: a per-idempotency-key mutex
// serializes check-then-insert so only the first request for a key creates
// an order; concurrent duplicates wait and replay the stored response.
//
// A production system would use a database unique constraint or an atomic
// compare-and-swap instead of an in-process mutex; the synchronization
// shape (first-writer wins, everyone else replays) is what matters here.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	OrderID  int    `json:"order_id"`
	ItemID   int    `json:"item_id"`
	Qty      int    `json:"qty"`
	Status   string `json:"status"`
	Response string `json:"response"` // volatile field to demo ignore_json
}

type stored struct {
	status      int
	body        []byte
	payloadHash string
}

type store struct {
	keyMu  sync.Mutex             // guards maps and per-key lock table
	locks  map[string]*sync.Mutex // one lock per idempotency key
	next   int
	orders map[string]stored // key -> stored response
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}
	s := &store{
		next:   400,
		locks:  map[string]*sync.Mutex{},
		orders: map[string]stored{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", s.handleCreate)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	log.Printf("safe-order-api listening on :%s", port)
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

	// Serialize all work for this key: duplicates queue up here and then
	// observe the first writer's stored response.
	mu := s.lockFor(key)
	mu.Lock()
	defer mu.Unlock()

	payloadHash := hash(body)
	if st, found := s.orders[key]; found {
		if st.payloadHash != payloadHash {
			// Same key, different payload: reject instead of silently
			// accepting two meanings for one key.
			httpError(w, http.StatusConflict, "idempotency key reused with a different payload")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st.status)
		w.Write(st.body)
		return
	}

	s.next++
	o := order{
		OrderID:  s.next,
		ItemID:   req.ItemID,
		Qty:      req.Qty,
		Status:   "created",
		Response: time.Now().UTC().Format(time.RFC3339Nano), // volatile on purpose
	}
	buf := &bytes.Buffer{}
	json.NewEncoder(buf).Encode(o)

	s.orders[key] = stored{status: http.StatusCreated, body: buf.Bytes(), payloadHash: payloadHash}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(buf.Bytes())
}

// lockFor returns the per-key mutex, creating it on first use.
func (s *store) lockFor(key string) *sync.Mutex {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	mu, ok := s.locks[key]
	if !ok {
		mu = &sync.Mutex{}
		s.locks[key] = mu
	}
	return mu
}

func hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
