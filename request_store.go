package gosmpp

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

// Request represent a request tracked by the RequestStore
type Request struct {
	pdu.PDU
	// TimeSent is the reservation time, available even before the write completes.
	TimeSent time.Time
	// WrittenAt is zero while the write is pending and records successful write completion.
	WrittenAt time.Time
	// Lineage identifies the session across connection generations; zero denotes
	// a reservation without a session lineage. Stores must preserve this value.
	Lineage uint64
	// Owner identifies the connection generation; ID identifies this reservation.
	// Both counters, and Lineage, start at a random 64-bit per-process prefix.
	// Stores must preserve both values unchanged, including across serialization.
	Owner uint64
	ID    uint64
}

// Response represents a response from a Request in the RequestStore
type Response struct {
	pdu.PDU
	OriginalRequest Request
	// Err is the decode error for a malformed response. PDU retains its response
	// type and received command/status/sequence header with no decoded body.
	// Check Err before interpreting body fields or treating IsOk as success.
	Err error
}

// ErrSequenceInUse means an outstanding reservation already owns the sequence number.
var ErrSequenceInUse = errors.New("sequence number already reserved")

// RequestStore interface used for WindowedRequestTracking
type RequestStore interface {
	// Set atomically inserts an absent sequence number. An existing reservation
	// must remain unchanged, and Set must return ErrSequenceInUse.
	Set(ctx context.Context, request Request) error
	// MarkWritten atomically sets WrittenAt only when the exact pending reservation
	// still exists. false means a response or another completion already removed it.
	// It must never insert an absent reservation or change a replacement.
	MarkWritten(ctx context.Context, request Request, sentAt time.Time) (bool, error)
	// Get returns false only for an absent sequence or canceled context. Respect
	// cancellation: recovery uses a successful absent read to settle uncertain Set.
	Get(ctx context.Context, sequenceNumber int32) (Request, bool)
	// List should return a complete snapshot and must respect cancellation. Stale
	// or truncated snapshots are supported for uncertain cleanup: Get confirms
	// absence or supplies the exact row to delete. A canceled List cannot do so.
	List(ctx context.Context) []Request
	// Delete atomically removes only the supplied reservation (sequence number,
	// Owner and ID must match). It reports whether it removed an entry.
	// The local ledger also arbitrates completion: a response finding the row
	// absent can claim it before expiry claims a successful removal.
	Delete(ctx context.Context, request Request) (bool, error)
	Clear(ctx context.Context) error
	Length(ctx context.Context) (int, error)
	// WrittenLength counts successfully written application requests, excluding
	// enquire_link and excludeOwner. An excludeOwner of zero includes all owners.
	// Writers add their local count for that owner, which remains authoritative
	// when a completed transport write cannot persist its WrittenAt metadata.
	WrittenLength(ctx context.Context, excludeOwner uint64) (int, error)
}

type DefaultStore struct{ state *defaultStoreState }

type defaultStoreState struct {
	mu           sync.RWMutex
	requests     map[int32]Request
	written      map[uint64]int
	totalWritten int
}

func NewDefaultStore() DefaultStore {
	return DefaultStore{state: &defaultStoreState{requests: make(map[int32]Request), written: make(map[uint64]int)}}
}

func countsInWindow(request Request) bool {
	_, enquiry := request.PDU.(*pdu.EnquireLink)
	return !enquiry && !request.WrittenAt.IsZero()
}

func (s DefaultStore) Set(ctx context.Context, request Request) error {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	key := request.GetSequenceNumber()
	if _, exists := s.state.requests[key]; exists {
		return ErrSequenceInUse
	}
	s.state.requests[key] = request
	if countsInWindow(request) {
		s.state.written[request.Owner]++
		s.state.totalWritten++
	}
	return nil
}

func (s DefaultStore) MarkWritten(ctx context.Context, request Request, sentAt time.Time) (bool, error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key := request.GetSequenceNumber()
	current, exists := s.state.requests[key]
	if !exists || !sameReservation(current, request) || !current.WrittenAt.IsZero() {
		return false, nil
	}
	current.WrittenAt = sentAt
	s.state.requests[key] = current
	if countsInWindow(current) {
		s.state.written[current.Owner]++
		s.state.totalWritten++
	}
	return true, nil
}

func sameReservation(current, request Request) bool {
	return current.Owner == request.Owner && current.ID == request.ID
}

func (s DefaultStore) Get(ctx context.Context, sequenceNumber int32) (Request, bool) {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	if ctx.Err() != nil {
		return Request{}, false
	}
	request, ok := s.state.requests[sequenceNumber]
	return request, ok
}

func (s DefaultStore) List(ctx context.Context) []Request {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	if ctx.Err() != nil {
		return nil
	}
	requests := make([]Request, 0, len(s.state.requests))
	for _, request := range s.state.requests {
		requests = append(requests, request)
	}
	return requests
}

func (s DefaultStore) Delete(ctx context.Context, request Request) (bool, error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key := request.GetSequenceNumber()
	current, exists := s.state.requests[key]
	if !exists || !sameReservation(current, request) {
		return false, nil
	}
	delete(s.state.requests, key)
	if countsInWindow(current) {
		s.state.totalWritten--
		s.state.written[current.Owner]--
		if s.state.written[current.Owner] == 0 {
			delete(s.state.written, current.Owner)
		}
	}
	return true, nil
}

func (s DefaultStore) Clear(ctx context.Context) error {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	clear(s.state.requests)
	clear(s.state.written)
	s.state.totalWritten = 0
	return nil
}

func (s DefaultStore) Length(ctx context.Context) (int, error) {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return len(s.state.requests), nil
}

func (s DefaultStore) WrittenLength(ctx context.Context, excludeOwner uint64) (int, error) {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	count := s.state.totalWritten
	if excludeOwner != 0 {
		count -= s.state.written[excludeOwner]
	}
	return count, nil
}
