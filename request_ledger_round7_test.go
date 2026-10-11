package gosmpp

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

const (
	ledgerHealthyRound7 = iota
	ledgerSetAckRound7
	ledgerDeleteAckRound7
	ledgerGetDeadlineRound7
	ledgerMarkFailureRound7
)

var errLedgerFaultRound7 = errors.New("ambiguous store acknowledgement")

// The immutable seeded schedule belongs to request sequences, so concurrent
// producer or worker ordering cannot change which faults a request encounters.
type ledgerFaultRound7Store struct {
	DefaultStore
	faults        map[int32]int
	healthy       atomic.Bool
	committedSets atomic.Int32
	appliedDelete atomic.Int32
	getDeadlines  atomic.Int32
	markFailures  atomic.Int32
}

func (s *ledgerFaultRound7Store) fault(sequence int32) int {
	if s.healthy.Load() {
		return ledgerHealthyRound7
	}
	return s.faults[sequence]
}

func (s *ledgerFaultRound7Store) Set(ctx context.Context, request Request) error {
	if s.fault(request.GetSequenceNumber()) == ledgerSetAckRound7 {
		if err := s.DefaultStore.Set(ctx, request); err != nil {
			return err
		}
		s.committedSets.Add(1)
		return errLedgerFaultRound7
	}
	return s.DefaultStore.Set(ctx, request)
}

func (s *ledgerFaultRound7Store) Delete(ctx context.Context, request Request) (bool, error) {
	if fault := s.fault(request.GetSequenceNumber()); fault == ledgerSetAckRound7 || fault == ledgerDeleteAckRound7 {
		removed, err := s.DefaultStore.Delete(ctx, request)
		if err != nil {
			return false, err
		}
		if removed {
			s.appliedDelete.Add(1)
		}
		return false, errLedgerFaultRound7
	}
	return s.DefaultStore.Delete(ctx, request)
}

func (s *ledgerFaultRound7Store) Get(ctx context.Context, sequence int32) (Request, bool) {
	if s.fault(sequence) == ledgerGetDeadlineRound7 {
		s.getDeadlines.Add(1)
		<-ctx.Done()
		return Request{}, false
	}
	return s.DefaultStore.Get(ctx, sequence)
}

func (s *ledgerFaultRound7Store) MarkWritten(ctx context.Context, request Request, at time.Time) (bool, error) {
	if s.fault(request.GetSequenceNumber()) == ledgerMarkFailureRound7 {
		s.markFailures.Add(1)
		return false, errLedgerFaultRound7
	}
	return s.DefaultStore.MarkWritten(ctx, request, at)
}

func assertLedgerRound7Invariant(t *testing.T, out *transmittable, empty bool) {
	t.Helper()
	// Transport barriers and joined producers make this a quiescent snapshot.
	out.ownerState.mu.Lock()
	defer out.ownerState.mu.Unlock()
	out.lineage.mu.Lock()
	defer out.lineage.mu.Unlock()
	uncertain := 0
	for id, current := range out.ownerState.requests {
		if !current.uncertain {
			continue
		}
		uncertain++
		marker, exists := out.lineage.abandoned.Load(id)
		if !exists {
			t.Errorf("uncertain identity %d has no cleanup marker", id)
			continue
		}
		request, ok := marker.(Request)
		if !ok || !sameReservation(request, current.Request) || request.Lineage != out.lineage.id {
			t.Errorf("uncertain identity %d has a mismatched cleanup marker", id)
		}
	}
	if out.lineage.unresolved != uncertain {
		t.Errorf("unresolved=%d; uncertain identities=%d", out.lineage.unresolved, uncertain)
	}
	if empty {
		markers := 0
		out.lineage.abandoned.Range(func(_, _ any) bool { markers++; return true })
		if len(out.ownerState.requests) != 0 || uncertain != 0 || markers != 0 || out.lineage.repairs.Load() != 0 {
			t.Errorf("healthy cleanup retained requests=%d uncertain=%d markers=%d metadata repairs=%d", len(out.ownerState.requests), uncertain, markers, out.lineage.repairs.Load())
		}
	} else if uncertain == 0 {
		t.Error("fault schedule produced no uncertain identities")
	}
}

type ledgerLineageRound7Store struct{ DefaultStore }

func (*ledgerLineageRound7Store) List(context.Context) []Request { return nil }

func TestRequestRecoveryPreservesOtherLineage(t *testing.T) {
	store := &ledgerLineageRound7Store{DefaultStore: NewDefaultStore()}
	out := recoveryProgressRound7Owner(t, store, lifecycleSettings())
	request := recoveryProgressRound7Uncertain(t, out, 1)[0]
	replacement := request
	replacement.Lineage++
	if err := store.Set(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	// A truncated List must not let absence resolution delete an identity
	// reported by Get as belonging to another lineage.
	forceCleanupScan(out)
	if current, exists := store.Get(context.Background(), replacement.GetSequenceNumber()); !exists || current.Lineage != replacement.Lineage {
		t.Fatal("uncertain cleanup deleted a reservation belonging to another lineage")
	}
	if retained, _ := cleanupOutstanding(out); retained != 0 {
		t.Fatal("replacement identity retained the original uncertain admission")
	}
}

func TestRequestLedgerInvariantWithStoreFaults(t *testing.T) {
	const producers, perProducer = 4, 32
	const total = producers * perProducer
	store := &ledgerFaultRound7Store{DefaultStore: NewDefaultStore(), faults: make(map[int32]int, total)}
	requests := make([]pdu.PDU, total)
	random := rand.New(rand.NewPCG(0x726f756e6437, 0x6c6564676572))
	for i := range requests {
		requests[i] = pdu.NewSubmitSM()
		fault := random.IntN(12)
		if fault > ledgerMarkFailureRound7 {
			fault = ledgerHealthyRound7
		}
		if i < 4 {
			fault = i + 1 // Always exercise every fault alongside the seeded mix.
		}
		store.faults[requests[i].GetSequenceNumber()] = fault
	}
	client, peer := net.Pipe()
	frames := make(chan pdu.PDU, total+4)
	readDone, writeDone := make(chan struct{}), make(chan struct{})
	batches := make(chan []pdu.PDU, 1)
	writeErrors := make(chan error, 1)
	go func() {
		defer close(readDone)
		for {
			frame, err := pdu.Parse(peer)
			if err != nil {
				return
			}
			frames <- frame
		}
	}()
	go func() {
		defer close(writeDone)
		for responses := range batches {
			for start := 0; start < len(responses); start += 4 {
				buffer := pdu.NewBuffer(nil)
				for i := min(start+4, len(responses)) - 1; i >= start; i-- {
					responses[i].Marshal(buffer)
				}
				if err := buffer.Err(); err != nil {
					writeErrors <- err
					return
				}
				if _, err := peer.Write(buffer.Bytes()); err != nil {
					writeErrors <- err
					return
				}
			}
			barrier := pdu.NewGenericNack()
			barrier.SetSequenceNumber(0)
			buffer := pdu.NewBuffer(nil)
			barrier.Marshal(buffer)
			if _, err := peer.Write(buffer.Bytes()); err != nil {
				writeErrors <- err
				return
			}
		}
	}()
	var completionMu sync.Mutex
	completions := make(map[int32]int)
	complete := func(request pdu.PDU) {
		completionMu.Lock()
		completions[request.GetSequenceNumber()]++
		completionMu.Unlock()
	}
	readerBarrier := make(chan struct{}, 1)
	settings := lifecycleSettings()
	settings.MaxWindowSize = total
	settings.StoreAccessTimeOut = 5 * time.Millisecond
	settings.ExpireCheckTimer = time.Hour
	settings.OnExpectedPduResponse = func(response Response) { complete(response.OriginalRequest.PDU) }
	settings.OnExpiredPduRequest = func(request pdu.PDU) bool { complete(request); return false }
	settings.OnClosePduRequest = complete
	settings.OnUnexpectedPduResponse = func(response pdu.PDU) {
		if response.GetSequenceNumber() == 0 {
			readerBarrier <- struct{}{}
		}
	}
	var session *Session
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			store.healthy.Store(true)
			if session != nil {
				_ = session.Close()
			}
			_ = client.Close()
			_ = peer.Close()
			close(batches)
			lifecycleWait(t, readDone)
			lifecycleWait(t, writeDone)
			if session != nil {
				liveRequestOwners.Delete(session.bound().out.owner)
			}
		})
	}
	t.Cleanup(cleanup)
	var err error
	session, err = NewSession(lifecycleConnector{connect: func() (*Connection, error) { return NewConnection(client), nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	out := session.bound().out
	out.reclaimMu.Lock()
	out.nextReclaim = time.Now().Add(time.Hour)
	out.reclaimMu.Unlock()
	start := make(chan struct{})
	var producersDone sync.WaitGroup
	var accepted atomic.Int32
	for producer := range producers {
		producersDone.Add(1)
		go func() {
			defer producersDone.Done()
			<-start
			for _, request := range requests[producer*perProducer : (producer+1)*perProducer] {
				if err := session.Transceiver().Submit(request); err == nil {
					accepted.Add(1)
				}
			}
		}()
	}
	close(start)
	producersDone.Wait()
	// This untracked frame passes through the same writer after every queued
	// request, including its metadata update and error reporting.
	writerBarrier := pdu.NewGenericNack()
	writerBarrier.SetSequenceNumber(0)
	if err := session.Transceiver().Submit(writerBarrier); err != nil {
		t.Fatal(err)
	}
	var responses []pdu.PDU
	for {
		frame := windowResponseFrame(t, frames)
		if frame.GetSequenceNumber() == 0 {
			break
		}
		responses = append(responses, frame.GetResponse())
	}
	if len(responses) != int(accepted.Load()) {
		t.Fatalf("accepted=%d wire requests=%d", accepted.Load(), len(responses))
	}
	if repairs, failures := out.lineage.repairs.Load(), store.markFailures.Load(); repairs != int64(failures) {
		t.Errorf("failed written markers retained metadata repairs=%d; want %d before peer responses", repairs, failures)
	}
	batches <- responses
	lifecycleWait(t, readerBarrier)
	select {
	case err := <-writeErrors:
		t.Fatal(err)
	default:
	}
	if store.committedSets.Load() == 0 || store.appliedDelete.Load() == 0 || store.getDeadlines.Load() == 0 || store.markFailures.Load() == 0 {
		t.Fatalf("incomplete fault coverage: committed Set=%d applied Delete=%d Get deadline=%d MarkWritten failure=%d", store.committedSets.Load(), store.appliedDelete.Load(), store.getDeadlines.Load(), store.markFailures.Load())
	}
	t.Logf("seeded fault run: accepted=%d committed Sets=%d applied Deletes=%d Get deadlines=%d MarkWritten failures=%d", accepted.Load(), store.committedSets.Load(), store.appliedDelete.Load(), store.getDeadlines.Load(), store.markFailures.Load())
	assertLedgerRound7Invariant(t, out, false)
	store.healthy.Store(true)
	for range 4 {
		forceCleanupScan(out)
	}
	assertLedgerRound7Invariant(t, out, true)
	cleanup()
	assertLedgerRound7Invariant(t, out, true)
	if rows, _ := store.Length(context.Background()); rows != 0 {
		t.Errorf("healthy final Close retained %d backend rows", rows)
	}
	completionMu.Lock()
	defer completionMu.Unlock()
	if len(completions) == 0 {
		t.Fatal("healthy responses produced no completion callbacks")
	}
	for sequence, count := range completions {
		if count != 1 {
			t.Errorf("sequence %d completed %d times", sequence, count)
		}
	}
}

// Keep the original 200-lineage footprint experiment out of normal test runs.
func BenchmarkRequestTombstoneRetention(b *testing.B) {
	const lineages, rejections = 200, 3000
	b.ReportAllocs()
	var retained uint64
	for range b.N {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		keep := make([]*requestLineage, lineages)
		for i := range keep {
			lineage := &requestLineage{id: uint64(i + 1)}
			for j := range rejections {
				lineage.rememberDiscarded(Request{PDU: pdu.NewSubmitSM(), ID: uint64(i*rejections + j + 1), Lineage: lineage.id})
			}
			if len(lineage.rejected) != maxUnresolvedRequests || len(lineage.rejections) != maxUnresolvedRequests {
				b.Fatalf("tombstone ring exceeded bound: map=%d ring=%d", len(lineage.rejected), len(lineage.rejections))
			}
			keep[i] = lineage
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		if after.HeapAlloc <= before.HeapAlloc {
			b.Fatal("retained lineage footprint was not positive")
		}
		retained += after.HeapAlloc - before.HeapAlloc
		runtime.KeepAlive(keep)
	}
	b.ReportMetric(float64(retained)/float64(b.N*lineages), "retained_B/lineage")
}
