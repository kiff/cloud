package cardwarnings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kiff/cloud/api/internal/alert"
	"github.com/kiff/cloud/api/internal/mandate"
)

type deliveryStore struct{ tried []string }

func (*deliveryStore) ScheduleWarningObservations(context.Context) error { return nil }
func (*deliveryStore) ProcessWarningWork(context.Context) error          { return nil }
func (*deliveryStore) PendingWarnings(context.Context) ([]mandate.CardWarning, error) {
	return []mandate.CardWarning{{ID: "bad", TenantID: "bad-account"}, {ID: "good", TenantID: "good-account"}}, nil
}
func (s *deliveryStore) DeliverWarning(_ context.Context, e mandate.CardWarning, _ func(context.Context, mandate.CardWarning) error, _ func(context.Context, mandate.CardWarning) ([]string, error), _ func(context.Context, mandate.CardWarning, string) error) error {
	s.tried = append(s.tried, e.ID)
	if e.ID == "bad" {
		return errors.New("recipient lookup unavailable")
	}
	return nil
}
func TestDeliverContinuesAfterOneAccountFails(t *testing.T) {
	s := &deliveryStore{}
	if err := (&Worker{Store: s}).Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.tried, ",") != "bad,good" {
		t.Fatalf("delivery stopped at failed account: %v", s.tried)
	}
}

type recordingSink struct {
	ids    []string
	bodies [][]byte
}

func (s *recordingSink) Send(_ context.Context, t alert.Target, b []byte) error {
	s.ids = append(s.ids, t.ID)
	s.bodies = append(s.bodies, b)
	return nil
}
func TestWebhookWarningsRequireExplicitOptIn(t *testing.T) {
	ctx := context.Background()
	targets := alert.NewMemoryStore()
	for _, target := range []alert.Target{
		{ID: "old-all", Enabled: true},
		{ID: "old-slack", Enabled: true, URL: "https://hooks.slack.com/services/example"},
		{ID: "by-action", Action: "card_budget", Enabled: true},
		{ID: "by-outcome", Outcomes: []string{"card_warning"}, Enabled: true},
		{ID: "disabled", Action: "card_budget"},
		{ID: "different-action", Action: "refund", Outcomes: []string{"card_warning"}, Enabled: true},
		{ID: "different-outcome", Action: "card_budget", Outcomes: []string{"blocked"}, Enabled: true},
	} {
		target.TenantID = "t"
		if err := targets.CreateTarget(ctx, target); err != nil {
			t.Fatal(err)
		}
	}
	sink := &recordingSink{}
	worker := &Worker{Targets: targets, Sink: sink, BaseURL: "https://app.example"}
	event := mandate.CardWarning{TenantID: "t", ID: "warning", CardID: "card"}
	destinations, err := worker.recipients(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(destinations, ",") != "webhook:by-action,webhook:by-outcome" {
		t.Fatalf("unexpected destinations: %v", destinations)
	}
	// Recheck at send time too, including destinations captured before opt-in.
	for _, destination := range append(destinations, "webhook:old-all", "webhook:old-slack") {
		if err := worker.send(ctx, event, destination); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(sink.ids, ",") != "by-action,by-outcome" {
		t.Fatalf("unsolicited webhook: %v", sink.ids)
	}
	for _, body := range sink.bodies {
		if !strings.Contains(string(body), `"card_url":"https://app.example/cards/card/warnings"`) {
			t.Fatalf("wrong review link: %s", body)
		}
	}
}

// slowScheduleStore's schedule runs out its time, as the prune did on a
// large table; processing and delivery still get their own.
type slowScheduleStore struct{ processed, delivered bool }

func (*slowScheduleStore) ScheduleWarningObservations(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
func (s *slowScheduleStore) ProcessWarningWork(ctx context.Context) error {
	s.processed = ctx.Err() == nil
	return nil
}
func (s *slowScheduleStore) PendingWarnings(ctx context.Context) ([]mandate.CardWarning, error) {
	s.delivered = ctx.Err() == nil
	return nil, nil
}
func (*slowScheduleStore) DeliverWarning(context.Context, mandate.CardWarning, func(context.Context, mandate.CardWarning) error, func(context.Context, mandate.CardWarning) ([]string, error), func(context.Context, mandate.CardWarning, string) error) error {
	return nil
}

func TestASlowScheduleLeavesTheOtherStagesTheirTime(t *testing.T) {
	s := &slowScheduleStore{}
	w := &Worker{Store: s, StageTimeout: 20 * time.Millisecond}
	now := time.Now()
	if last := w.pass(context.Background(), now, time.Time{}); !last.IsZero() {
		t.Fatalf("a failed schedule counted as scheduled at %v", last)
	}
	if !s.processed || !s.delivered {
		t.Fatalf("processed %v, delivered %v: a slow schedule took the other stages' time", s.processed, s.delivered)
	}
}

// stopStore blocks its schedule stage until released, and records whether
// each stage ran on a live context.
type stopStore struct {
	started, release   chan struct{}
	passes             int
	scheduled, process bool
	deliver            bool
	waitForCancel      bool
}

func (s *stopStore) ScheduleWarningObservations(ctx context.Context) error {
	s.passes++
	if s.passes == 1 {
		close(s.started)
		if s.waitForCancel {
			<-ctx.Done()
			return ctx.Err()
		}
		<-s.release
	}
	s.scheduled = ctx.Err() == nil
	return nil
}
func (s *stopStore) ProcessWarningWork(ctx context.Context) error {
	s.process = ctx.Err() == nil
	return nil
}
func (s *stopStore) PendingWarnings(ctx context.Context) ([]mandate.CardWarning, error) {
	s.deliver = ctx.Err() == nil
	return nil, nil
}
func (*stopStore) DeliverWarning(context.Context, mandate.CardWarning, func(context.Context, mandate.CardWarning) error, func(context.Context, mandate.CardWarning) ([]string, error), func(context.Context, mandate.CardWarning, string) error) error {
	return nil
}

// The API's stop ends Run's context. A pass under way finishes its stages
// on a live context, rather than being cut off and logged, and no new pass
// starts.
func TestAPassUnderWayFinishesWhenTheWorkerStops(t *testing.T) {
	s := &stopStore{started: make(chan struct{}), release: make(chan struct{})}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); (&Worker{Store: s}).Run(ctx) }()
	<-s.started
	stop()
	time.Sleep(20 * time.Millisecond)
	close(s.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't return after its pass")
	}
	if !s.scheduled || !s.process || !s.deliver || s.passes != 1 {
		t.Fatalf("scheduled %v, processed %v, delivered %v, passes %d", s.scheduled, s.process, s.deliver, s.passes)
	}
}

// A stage that doesn't finish is cut off once the grace is used up, so a
// stop never waits on it for long.
func TestAStopCutsAPassOffAfterTheGrace(t *testing.T) {
	s := &stopStore{started: make(chan struct{}), waitForCancel: true}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Worker{Store: s, ShutdownGrace: 50 * time.Millisecond, StageTimeout: time.Minute}).Run(ctx)
	}()
	<-s.started
	stopped := time.Now()
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run waited past its grace")
	}
	if waited := time.Since(stopped); waited < 50*time.Millisecond {
		t.Fatalf("the pass was cut off after %v, before its grace", waited)
	}
}

type failingStore struct {
	stopStore
	err error
}

func (s *failingStore) ProcessWarningWork(context.Context) error { return s.err }

// A deadlock or a serialization failure is kept and tried again later (the
// work's backoff), so it's logged as information; any other failure warns.
func TestADeadlockIsLoggedAsTriedAgain(t *testing.T) {
	for name, c := range map[string]struct {
		err   error
		level slog.Level
	}{
		"a deadlock":        {fmt.Errorf("observe: %w", &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}), slog.LevelInfo},
		"a serialization":   {&pgconn.PgError{Code: "40001"}, slog.LevelInfo},
		"a failure":         {errors.New("connection refused"), slog.LevelWarn},
		"another SQL error": {&pgconn.PgError{Code: "23505"}, slog.LevelWarn},
	} {
		var logs bytes.Buffer
		w := &Worker{Store: &failingStore{err: c.err}, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
		w.pass(context.Background(), time.Now(), time.Now())
		var line struct {
			Level slog.Level `json:"level"`
			Msg   string     `json:"msg"`
		}
		if err := json.Unmarshal(bytes.SplitN(logs.Bytes(), []byte("\n"), 2)[0], &line); err != nil || line.Level != c.level || !strings.HasPrefix(line.Msg, "Card warnings: accounting observation") {
			t.Errorf("%s: %+v %v\n%s", name, line, err, logs.String())
		}
	}
}
