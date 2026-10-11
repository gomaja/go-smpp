package gosmpp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

type cleanupOutageStore struct {
	DefaultStore
	outage      atomic.Bool
	sets        atomic.Int32
	lists       atomic.Int32
	getTimeout  atomic.Bool
	listTimeout atomic.Bool
	gets        atomic.Int32
}

func (s *cleanupOutageStore) Set(ctx context.Context, request Request) error {
	s.sets.Add(1)
	if s.outage.Load() {
		return errors.New("store unavailable")
	}
	return s.DefaultStore.Set(ctx, request)
}
func (s *cleanupOutageStore) Delete(ctx context.Context, request Request) (bool, error) {
	if s.outage.Load() {
		return false, errors.New("store unavailable")
	}
	return s.DefaultStore.Delete(ctx, request)
}
func (s *cleanupOutageStore) List(ctx context.Context) []Request {
	s.lists.Add(1)
	if s.listTimeout.Load() {
		<-ctx.Done()
		return nil
	}
	return s.DefaultStore.List(ctx)
}
func (s *cleanupOutageStore) Get(ctx context.Context, sequence int32) (Request, bool) {
	s.gets.Add(1)
	if s.getTimeout.Load() {
		<-ctx.Done()
		return Request{}, false
	}
	return s.DefaultStore.Get(ctx, sequence)
}

func cleanupOutstanding(out *transmittable) (int, int) {
	out.ownerState.mu.Lock()
	requests := len(out.ownerState.requests)
	out.ownerState.mu.Unlock()
	abandoned := 0
	out.lineage.abandoned.Range(func(_, _ any) bool { abandoned++; return true })
	return requests, abandoned
}

func forceCleanupScan(out *transmittable) {
	out.reclaimMu.Lock()
	out.nextReclaim = time.Time{}
	out.reclaimMu.Unlock()
	out.reclaimDeadRequests()
	out.reclaimWG.Wait()
}

func TestWindowUncertainAdmissionsResolveByAbsence(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &cleanupOutageStore{DefaultStore: NewDefaultStore()}
	store.outage.Store(true)
	settings := lifecycleSettings()
	var completed atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { completed.Add(1) }
	out := newTransmittable(conn, settings, store)
	defer func() { _ = out.close(ExplicitClosing) }()
	// Keep setup scans from overlapping the fresh healthy-store scan below.
	out.reclaimMu.Lock()
	out.nextReclaim = time.Now().Add(time.Hour)
	out.reclaimMu.Unlock()
	for range 1000 {
		if err := out.Submit(pdu.NewSubmitSM()); err == nil {
			t.Fatal("outage accepted request")
		}
	}
	store.outage.Store(false)
	out.reclaimWG.Wait()
	if scans := store.lists.Load(); scans != 0 {
		t.Fatalf("setup started %d recovery scans before the fresh cleanup pass", scans)
	}
	// Four bounded scans cover all 1000 unresolved admissions.
	for range 4 {
		forceCleanupScan(out)
	}
	requests, abandoned := cleanupOutstanding(out)
	if requests != 0 || abandoned != 0 || out.lineage.orphaned.Load() {
		t.Fatalf("absent uncertain admissions retained: requests=%d abandoned=%d hint=%v", requests, abandoned, out.lineage.orphaned.Load())
	}
	if completed.Load() != 0 {
		t.Fatal("rejected admissions produced completion callbacks")
	}
}

func TestWindowUncertainAdmissionsAreBounded(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &cleanupOutageStore{DefaultStore: NewDefaultStore()}
	store.outage.Store(true)
	out := newTransmittable(conn, lifecycleSettings(), store)
	defer func() { store.outage.Store(false); _ = out.close(ExplicitClosing) }()
	// Keep the recovery throttle closed while measuring the retained bound.
	out.reclaimMu.Lock()
	out.nextReclaim = time.Now().Add(time.Hour)
	out.reclaimMu.Unlock()
	for range 1100 {
		if err := out.Submit(pdu.NewSubmitSM()); err == nil {
			t.Fatal("outage accepted request")
		}
	}
	requests, abandoned := cleanupOutstanding(out)
	if requests > 1024 || abandoned > 1024 || store.sets.Load() > 1024 {
		t.Fatalf("unbounded outage accounting: requests=%d abandoned=%d Set calls=%d", requests, abandoned, store.sets.Load())
	}
}

func TestWindowUncertainAbsencePreservesReplacement(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &cleanupOutageStore{DefaultStore: NewDefaultStore()}
	store.outage.Store(true)
	out := newTransmittable(conn, lifecycleSettings(), store)
	defer func() { _ = out.close(ExplicitClosing) }()
	p := pdu.NewSubmitSM()
	if err := out.Submit(p); err == nil {
		t.Fatal("outage accepted request")
	}
	store.outage.Store(false)
	other := pdu.NewSubmitSM()
	other.SetSequenceNumber(p.GetSequenceNumber())
	replacement := Request{PDU: other, Owner: ^out.owner, ID: ^uint64(0), Lineage: ^out.lineage.id}
	if err := store.Set(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	forceCleanupScan(out)
	requests, abandoned := cleanupOutstanding(out)
	if requests != 0 || abandoned != 0 || out.lineage.orphaned.Load() {
		t.Fatalf("replacement retained uncertainty: %d/%d hint=%v", requests, abandoned, out.lineage.orphaned.Load())
	}
	retained, ok := store.Get(context.Background(), other.GetSequenceNumber())
	if !ok || retained.ID != replacement.ID {
		t.Fatal("recovery deleted replacement")
	}
}

func TestWindowUncertainReplacementUsesFullIdentity(t *testing.T) {
	for _, field := range []string{"Owner", "ID"} {
		t.Run(field, func(t *testing.T) {
			conn, cleanup := newPipePair()
			defer cleanup()
			store := &cleanupOutageStore{DefaultStore: NewDefaultStore()}
			store.outage.Store(true)
			out := newTransmittable(conn, lifecycleSettings(), store)
			defer func() { _ = out.close(ExplicitClosing) }()
			if err := out.Submit(pdu.NewSubmitSM()); err == nil {
				t.Fatal("outage accepted request")
			}
			var replacement Request
			out.ownerState.mu.Lock()
			for _, request := range out.ownerState.requests {
				replacement = Request{PDU: request.PDU, TimeSent: request.TimeSent, Owner: request.Owner, ID: request.ID, Lineage: request.Lineage}
				break
			}
			out.ownerState.mu.Unlock()
			switch field {
			case "Owner":
				replacement.Owner = ^replacement.Owner
				replacement.Lineage = ^replacement.Lineage
			case "ID":
				replacement.ID = ^replacement.ID
			}
			store.outage.Store(false)
			if err := store.Set(context.Background(), replacement); err != nil {
				t.Fatal(err)
			}
			forceCleanupScan(out)
			requests, abandoned := cleanupOutstanding(out)
			if requests != 0 || abandoned != 0 {
				t.Fatalf("%s replacement retained uncertainty: %d/%d", field, requests, abandoned)
			}
			if retained, exists := store.Get(context.Background(), replacement.GetSequenceNumber()); !exists || retained != replacement {
				t.Fatal("replacement was modified or removed")
			}
		})
	}
}

func TestWindowRecoveryPreservesUnknownProcessOwner(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := NewDefaultStore()
	out := newTransmittable(conn, lifecycleSettings(), store)
	defer func() { _ = out.close(ExplicitClosing) }()
	request := out.newRequest(pdu.NewSubmitSM())
	request.Owner, request.Lineage = ^request.Owner, ^request.Lineage
	if err := store.Set(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	forceCleanupScan(out)
	if _, exists := store.Get(context.Background(), request.GetSequenceNumber()); !exists {
		t.Fatal("unknown process's request was reclaimed without a local owner ledger")
	}
}

func TestWindowUncertainGetTimeoutRetainsCleanupObligation(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &cleanupOutageStore{DefaultStore: NewDefaultStore()}
	store.outage.Store(true)
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 10 * time.Millisecond
	out := newTransmittable(conn, settings, store)
	defer func() { store.getTimeout.Store(false); _ = out.close(ExplicitClosing) }()
	if err := out.Submit(pdu.NewSubmitSM()); err == nil {
		t.Fatal("outage accepted request")
	}
	store.outage.Store(false)
	store.getTimeout.Store(true)
	forceCleanupScan(out)
	requests, abandoned := cleanupOutstanding(out)
	if requests != 1 || abandoned != 1 || !out.lineage.orphaned.Load() {
		t.Fatalf("failed Get lost cleanup obligation: %d/%d hint=%v", requests, abandoned, out.lineage.orphaned.Load())
	}
	store.getTimeout.Store(false)
	forceCleanupScan(out)
	requests, abandoned = cleanupOutstanding(out)
	if requests != 0 || abandoned != 0 {
		t.Fatalf("healthy Get did not resolve absence: %d/%d", requests, abandoned)
	}
}

func TestWindowUncertainIndependentGetResolvesAbsenceDespiteListTimeout(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &cleanupOutageStore{DefaultStore: NewDefaultStore()}
	store.outage.Store(true)
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 10 * time.Millisecond
	out := newTransmittable(conn, settings, store)
	defer func() { store.listTimeout.Store(false); _ = out.close(ExplicitClosing) }()
	if err := out.Submit(pdu.NewSubmitSM()); err == nil {
		t.Fatal("outage accepted request")
	}
	store.outage.Store(false)
	store.listTimeout.Store(true)
	forceCleanupScan(out)
	requests, abandoned := cleanupOutstanding(out)
	if requests != 0 || abandoned != 0 || store.gets.Load() != 1 || !out.lineage.orphaned.Load() {
		t.Fatalf("independent Get failed to establish absence: requests=%d abandoned=%d Get=%d hint=%v", requests, abandoned, store.gets.Load(), out.lineage.orphaned.Load())
	}
	store.listTimeout.Store(false)
	forceCleanupScan(out)
	requests, abandoned = cleanupOutstanding(out)
	if requests != 0 || abandoned != 0 || out.lineage.orphaned.Load() {
		t.Fatal("healthy List failed to resolve uncertainty")
	}
}

func TestWindowUncertainScanStopsAtFirstUnavailableLookup(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &cleanupOutageStore{DefaultStore: NewDefaultStore()}
	store.outage.Store(true)
	settings := lifecycleSettings()
	// Give local scan bookkeeping time to reach the intentionally blocked Get.
	// That Get still waits for the real store deadline; this checks lookup count.
	settings.StoreAccessTimeOut = 100 * time.Millisecond
	out := newTransmittable(conn, settings, store)
	defer func() { store.getTimeout.Store(false); _ = out.close(ExplicitClosing) }()
	out.reclaimMu.Lock()
	out.nextReclaim = time.Now().Add(time.Hour)
	out.reclaimMu.Unlock()
	for range 8 {
		if err := out.Submit(pdu.NewSubmitSM()); err == nil {
			t.Fatal("outage accepted request")
		}
	}
	store.outage.Store(false)
	store.getTimeout.Store(true)
	forceCleanupScan(out)
	requests, abandoned := cleanupOutstanding(out)
	if requests != 8 || abandoned != 8 || store.gets.Load() != 1 {
		t.Fatalf("unavailable scan consumed multiple deadlines: requests=%d abandoned=%d Get=%d", requests, abandoned, store.gets.Load())
	}
}

type cleanupStickyOwnerStore struct {
	DefaultStore
	stuck atomic.Int32
}

func (s *cleanupStickyOwnerStore) Delete(ctx context.Context, r Request) (bool, error) {
	if r.GetSequenceNumber() == s.stuck.Load() {
		return false, errors.New("one row unavailable")
	}
	return s.DefaultStore.Delete(ctx, r)
}

func TestWindowForeignCleanupRequiresExactDeadOwnerIdentity(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovery", true: "expiry"}[expiry], func(t *testing.T) {
			store := &cleanupStickyOwnerStore{DefaultStore: NewDefaultStore()}
			firstConn, cleanFirst := newPipePair()
			defer cleanFirst()
			secondConn, cleanSecond := newPipePair()
			defer cleanSecond()
			settings := lifecycleSettings()
			settings.PduExpireTimeOut = time.Nanosecond
			first := newTransceivable(firstConn, settings, store)
			if _, err := first.out.write(pdu.NewSubmitSM()); err != nil {
				t.Fatal(err)
			}
			entries := store.List(context.Background())
			store.stuck.Store(entries[0].GetSequenceNumber())
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { liveRequestOwners.Delete(first.out.owner) })
			original := entries[0]
			if removed, err := store.DefaultStore.Delete(context.Background(), original); err != nil || !removed {
				t.Fatalf("replace stored row: removed=%v err=%v", removed, err)
			}
			foreign := original
			foreign.ID = ^original.ID
			foreign.WrittenAt = time.Now().Add(-time.Hour)
			if err := store.Set(context.Background(), foreign); err != nil {
				t.Fatal(err)
			}
			store.stuck.Store(0)
			second := newTransceivable(secondConn, settings, store)
			defer func() { _ = second.Close() }()
			if expiry {
				second.expireRequests()
			} else {
				forceCleanupScan(second.out)
			}
			if _, ok := store.Get(context.Background(), foreign.GetSequenceNumber()); !ok {
				t.Fatal("another process's row was deleted because Owner collided")
			}
		})
	}
}

func TestWindowForeignCleanupRequiresMatchingSequence(t *testing.T) {
	store := &cleanupStickyOwnerStore{DefaultStore: NewDefaultStore()}
	firstConn, cleanFirst := newPipePair()
	defer cleanFirst()
	secondConn, cleanSecond := newPipePair()
	defer cleanSecond()
	settings := lifecycleSettings()
	first := newTransceivable(firstConn, settings, store)
	message := pdu.NewSubmitSM()
	if _, err := first.out.write(message); err != nil {
		t.Fatal(err)
	}
	original, _ := store.Get(context.Background(), message.GetSequenceNumber())
	store.stuck.Store(original.GetSequenceNumber())
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveRequestOwners.Delete(first.out.owner) })
	collision := original
	collision.PDU = pdu.NewSubmitSM()
	if err := store.Set(context.Background(), collision); err != nil {
		t.Fatal(err)
	}
	second := newTransceivable(secondConn, settings, store)
	defer func() { _ = second.Close() }()
	forceCleanupScan(second.out)
	if _, exists := store.Get(context.Background(), collision.GetSequenceNumber()); !exists {
		t.Fatal("foreign cleanup removed a sequence absent from the dead owner's ledger")
	}
}

func TestWindowForeignCleanupRequiresMatchingLineage(t *testing.T) {
	store := &cleanupStickyOwnerStore{DefaultStore: NewDefaultStore()}
	firstConn, cleanFirst := newPipePair()
	defer cleanFirst()
	secondConn, cleanSecond := newPipePair()
	defer cleanSecond()
	settings := lifecycleSettings()
	first := newTransceivable(firstConn, settings, store)
	message := pdu.NewSubmitSM()
	if _, err := first.out.write(message); err != nil {
		t.Fatal(err)
	}
	original, _ := store.Get(context.Background(), message.GetSequenceNumber())
	store.stuck.Store(original.GetSequenceNumber())
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveRequestOwners.Delete(first.out.owner) })
	if removed, err := store.DefaultStore.Delete(context.Background(), original); err != nil || !removed {
		t.Fatalf("replace stored row: removed=%v err=%v", removed, err)
	}
	collision := original
	collision.Lineage = ^original.Lineage
	if err := store.Set(context.Background(), collision); err != nil {
		t.Fatal(err)
	}
	store.stuck.Store(0)
	second := newTransceivable(secondConn, settings, store)
	defer func() { _ = second.Close() }()
	forceCleanupScan(second.out)
	if _, exists := store.Get(context.Background(), collision.GetSequenceNumber()); !exists {
		t.Fatal("foreign cleanup removed a row outside the dead owner's lineage")
	}
}

type cleanupFailedRandom struct{}

func (cleanupFailedRandom) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestWindowProcessIdentityPrefixAndFallback(t *testing.T) {
	if got := requestProcessPrefix(strings.NewReader("abcdefgh"), 1, 2); got != 0x6162636465666768 {
		t.Fatalf("entropy prefix=%x", got)
	}
	if got := requestProcessPrefix(cleanupFailedRandom{}, 1, 2); got == 0 {
		t.Fatal("fallback identity is zero")
	}
	if requestProcessPrefix(cleanupFailedRandom{}, 1, 2) == requestProcessPrefix(cleanupFailedRandom{}, 1, 3) {
		t.Fatal("fallback ignores process ID")
	}
	if requestProcessPrefix(cleanupFailedRandom{}, 1, 2) == requestProcessPrefix(cleanupFailedRandom{}, 2, 2) {
		t.Fatal("fallback ignores time")
	}
	if requestProcessPrefix(strings.NewReader("\x00\x00\x00\x00\x00\x00\x00\x00"), 1, 2) == 0 {
		t.Fatal("zero entropy prefix not replaced")
	}
	if got, want := requestProcessPrefix(strings.NewReader("abcdefg"), 1, 2), requestProcessPrefix(cleanupFailedRandom{}, 1, 2); got != want {
		t.Fatalf("partial entropy bypassed fallback: got=%x want=%x", got, want)
	}
	if requestProcessPrefix(cleanupFailedRandom{}, 0, 0) == 0 {
		t.Fatal("degenerate fallback identity is zero")
	}
}

func TestWindowProcessIdentitiesDifferAcrossProcesses(t *testing.T) {
	if os.Getenv("SMPP_TEST_IDENTITY_CHILD") == "1" {
		out := newTransmittable(nil, Settings{}, nil)
		request := out.newRequest(pdu.NewSubmitSM())
		fmt.Printf("request-identity=%x/%x/%x\n", request.Owner, request.Lineage, request.ID)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	identities := make([]string, 2)
	for i := range identities {
		command := exec.Command(executable, "-test.run=^TestWindowProcessIdentitiesDifferAcrossProcesses$")
		command.Env = append(os.Environ(), "SMPP_TEST_IDENTITY_CHILD=1", "GORACE=atexit_sleep_ms=0")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("identity process: %v: %s", err, output)
		}
		for _, line := range strings.Split(string(output), "\n") {
			if strings.HasPrefix(line, "request-identity=") {
				identities[i] = line
			}
		}
		if identities[i] == "" {
			t.Fatalf("missing child identity: %s", output)
		}
	}
	first := strings.Split(strings.TrimPrefix(identities[0], "request-identity="), "/")
	second := strings.Split(strings.TrimPrefix(identities[1], "request-identity="), "/")
	for i, name := range []string{"Owner", "Lineage", "ID"} {
		if first[i] == second[i] {
			t.Fatalf("independent processes reused %s: %s and %s", name, identities[0], identities[1])
		}
	}
}
