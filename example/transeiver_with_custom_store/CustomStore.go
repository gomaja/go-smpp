package main

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/allegro/bigcache/v3"
	"github.com/gomaja/go-smpp/pdu"

	gosmpp "github.com/gomaja/go-smpp"
)

// CustomStore demonstrates a serialized RequestStore backed by bigcache.
// The shared mutex makes reservation comparison and removal one operation,
// including when a CustomStore value is copied into an interface.
// Cache eviction must not complete a request: the serialized reservation index
// retains each request until Delete or Clear explicitly removes it.
type CustomStore struct {
	store        *bigcache.BigCache
	mu           *sync.RWMutex
	reservations map[string][]byte
	written      *writtenCounts
}

var _ gosmpp.RequestStore = (*CustomStore)(nil)

type writtenCounts struct {
	total  int
	owners map[uint64]int
}

func NewCustomStore() (CustomStore, error) {
	config := bigcache.DefaultConfig(30 * time.Second)
	// Set can evict old cached copies; no cleanup goroutine is needed because
	// the reservation index controls request lifetime.
	config.CleanWindow = 0
	cache, err := bigcache.New(context.Background(), config)
	if err != nil {
		return CustomStore{}, err
	}
	return CustomStore{
		store:        cache,
		mu:           &sync.RWMutex{},
		reservations: make(map[string][]byte),
		written:      &writtenCounts{owners: make(map[uint64]int)},
	}, nil
}

func (s CustomStore) Set(ctx context.Context, request gosmpp.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		key := strconv.Itoa(int(request.PDU.GetSequenceNumber()))
		if _, exists := s.reservations[key]; exists {
			return gosmpp.ErrSequenceInUse
		}
		b, err := serialize(request)
		if err != nil {
			return err
		}
		if err := s.store.Set(key, b); err != nil {
			return err
		}
		s.reservations[key] = b
		s.countWritten(request, 1)
		return nil
	}
}

func (s CustomStore) MarkWritten(ctx context.Context, request gosmpp.Request, sentAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key := strconv.Itoa(int(request.GetSequenceNumber()))
	encoded, exists := s.reservations[key]
	if !exists {
		return false, nil
	}
	current, err := deserialize(encoded)
	if err != nil {
		return false, err
	}
	if current.Owner != request.Owner || current.ID != request.ID || !current.WrittenAt.IsZero() {
		return false, nil
	}
	current.WrittenAt = sentAt
	encoded, err = serialize(current)
	if err != nil {
		return false, err
	}
	if err = s.store.Set(key, encoded); err != nil {
		return false, err
	}
	s.reservations[key] = encoded
	s.countWritten(current, 1)
	return true, nil
}

func (s CustomStore) Get(ctx context.Context, sequenceNumber int32) (gosmpp.Request, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	select {
	case <-ctx.Done():
		return gosmpp.Request{}, false
	default:
		key := strconv.Itoa(int(sequenceNumber))
		reserved, exists := s.reservations[key]
		if !exists {
			return gosmpp.Request{}, false
		}
		if cached, err := s.store.Get(key); err == nil {
			if request, err := deserialize(cached); err == nil {
				return request, true
			}
		}
		request, err := deserialize(reserved)
		if err != nil {
			// Get has no error result; undecodable data is unusable as a request.
			return gosmpp.Request{}, false
		}
		return request, true
	}
}

func (s CustomStore) List(ctx context.Context) []gosmpp.Request {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var requests []gosmpp.Request
	select {
	case <-ctx.Done():
		return requests
	default:
		for _, encoded := range s.reservations {
			request, err := deserialize(encoded)
			if err == nil {
				requests = append(requests, request)
			}
		}
		return requests
	}
}

func (s CustomStore) Delete(ctx context.Context, request gosmpp.Request) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
		key := strconv.Itoa(int(request.GetSequenceNumber()))
		encoded, exists := s.reservations[key]
		if !exists {
			return false, nil
		}
		current, err := deserialize(encoded)
		if err != nil {
			return false, err
		}
		if current.Owner != request.Owner || current.ID != request.ID {
			return false, nil
		}
		if err := s.store.Delete(key); err != nil && !errors.Is(err, bigcache.ErrEntryNotFound) {
			return false, err
		}
		delete(s.reservations, key)
		s.countWritten(current, -1)
		return true, nil
	}
}

func (s CustomStore) Clear(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		err := s.store.Reset()
		if err != nil {
			return err
		}
		clear(s.reservations)
		clear(s.written.owners)
		s.written.total = 0
		return nil
	}
}

func (s CustomStore) Length(ctx context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
		return len(s.reservations), nil
	}
}

func serialize(request gosmpp.Request) ([]byte, error) {
	buf := pdu.NewBuffer(make([]byte, 0, 64))
	request.PDU.Marshal(buf)
	if err := buf.Err(); err != nil {
		return nil, err
	}
	b := bytes.Buffer{}
	e := gob.NewEncoder(&b)
	err := e.Encode(requestGob{
		Pdu:       buf.Bytes(),
		TimeSent:  request.TimeSent,
		WrittenAt: request.WrittenAt,
		Lineage:   request.Lineage,
		Owner:     request.Owner,
		ID:        request.ID,
	})
	if err != nil {
		return b.Bytes()[:], errors.New("serialization failed")
	}
	return b.Bytes(), nil
}

func deserialize(bRequest []byte) (request gosmpp.Request, err error) {
	r := requestGob{}
	b := bytes.Buffer{}
	_, err = b.Write(bRequest)
	if err != nil {
		return request, errors.New("deserialization failed")
	}
	d := gob.NewDecoder(&b)
	err = d.Decode(&r)
	if err != nil {
		return request, errors.New("deserialization failed")
	}
	p, err := pdu.Parse(bytes.NewReader(r.Pdu))
	if err != nil {
		return gosmpp.Request{}, err
	}
	return gosmpp.Request{
		PDU:       p,
		TimeSent:  r.TimeSent,
		WrittenAt: r.WrittenAt,
		Lineage:   r.Lineage,
		Owner:     r.Owner,
		ID:        r.ID,
	}, nil
}

type requestGob struct {
	Pdu       []byte
	TimeSent  time.Time
	WrittenAt time.Time
	Lineage   uint64
	Owner     uint64
	ID        uint64
}

func (s CustomStore) countWritten(request gosmpp.Request, delta int) {
	if request.WrittenAt.IsZero() {
		return
	}
	if _, enquiry := request.PDU.(*pdu.EnquireLink); enquiry {
		return
	}
	s.written.total += delta
	s.written.owners[request.Owner] += delta
	if s.written.owners[request.Owner] == 0 {
		delete(s.written.owners, request.Owner)
	}
}

func (s CustomStore) WrittenLength(ctx context.Context, excludeOwner uint64) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	count := s.written.total
	if excludeOwner != 0 {
		count -= s.written.owners[excludeOwner]
	}
	return count, nil
}
