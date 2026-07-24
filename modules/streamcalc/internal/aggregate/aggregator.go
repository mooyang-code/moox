package aggregate

import (
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/streamcalc/internal/config"
	"github.com/mooyang-code/moox/packages/events/marketpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type WindowKey struct {
	SpaceID   string
	Subject   string
	Frequency string
	Start     time.Time
}

type Bar struct {
	Key         WindowKey
	Open        float64
	High        float64
	Low         float64
	Close       float64
	Volume      float64
	QuoteVolume float64
	TradeCount  int64
	Revision    uint64
	Closed      bool
	InputIDs    map[string]struct{}
	FirstInput  time.Time `json:"first_input,omitempty"`
	LastInput   time.Time `json:"last_input,omitempty"`
}

type Result struct {
	Bar       Bar
	Duplicate bool
	Late      bool
}

type Snapshot struct {
	Windows []BarSnapshot  `json:"windows"`
	Pending map[string]Bar `json:"pending,omitempty"`
}

type BarSnapshot struct {
	Bar      Bar       `json:"bar"`
	ClosedAt time.Time `json:"closed_at,omitempty"`
}

type Aggregator struct {
	mu              sync.Mutex
	inputFrequency  time.Duration
	targetFrequency time.Duration
	allowedLateness time.Duration
	windows         map[WindowKey]*Bar
	closedAt        map[WindowKey]time.Time
}

func New(inputFrequency, targetFrequency string, allowedLateness time.Duration) (*Aggregator, error) {
	input, err := config.ParseFrequency(inputFrequency)
	if err != nil {
		return nil, err
	}
	target, err := config.ParseFrequency(targetFrequency)
	if err != nil {
		return nil, err
	}
	if target < input || target%input != 0 {
		return nil, fmt.Errorf("target frequency %s must be a multiple of input frequency %s", target, input)
	}
	if allowedLateness < 0 {
		return nil, fmt.Errorf("allowed lateness must not be negative")
	}
	return &Aggregator{
		inputFrequency: input, targetFrequency: target, allowedLateness: allowedLateness,
		windows: make(map[WindowKey]*Bar), closedAt: make(map[WindowKey]time.Time),
	}, nil
}

func (a *Aggregator) Apply(eventID, spaceID string, input *marketpb.KlineClosed) (Result, error) {
	if a == nil || input == nil {
		return Result{}, fmt.Errorf("aggregator or input is nil")
	}
	if eventID == "" || spaceID == "" || input.GetSymbol() == "" || input.GetFrequency() == "" || input.GetWindowStart() == nil || input.GetWindowEnd() == nil {
		return Result{}, fmt.Errorf("kline event has incomplete identity or window")
	}
	start := input.GetWindowStart().AsTime().UTC()
	end := input.GetWindowEnd().AsTime().UTC()
	if !end.After(start) {
		return Result{}, fmt.Errorf("kline window end must be after start")
	}
	if end.Sub(start) != a.inputFrequency {
		return Result{}, fmt.Errorf("input kline duration %s does not match configured %s", end.Sub(start), a.inputFrequency)
	}
	windowStart := start.Truncate(a.targetFrequency)
	key := WindowKey{SpaceID: spaceID, Subject: input.GetSymbol(), Frequency: formatFrequency(a.targetFrequency), Start: windowStart}

	a.mu.Lock()
	defer a.mu.Unlock()
	bar := a.windows[key]
	if bar == nil {
		bar = &Bar{
			Key: key, Open: input.GetOpen(), Close: input.GetClose(), FirstInput: start, LastInput: start,
			InputIDs: map[string]struct{}{},
		}
		a.windows[key] = bar
	}
	if _, ok := bar.InputIDs[eventID]; ok {
		return Result{Bar: cloneBar(bar), Duplicate: true}, nil
	}
	if closedAt, ok := a.closedAt[key]; ok && start.After(closedAt.Add(a.allowedLateness)) {
		return Result{Bar: cloneBar(bar), Late: true}, nil
	}
	bar.InputIDs[eventID] = struct{}{}
	if start.Before(windowStart) {
		return Result{}, fmt.Errorf("input window starts before aggregate window")
	}
	if start.Before(bar.FirstInput) || start.Equal(windowStart) {
		bar.Open = input.GetOpen()
		bar.FirstInput = start
	}
	if input.GetHigh() > bar.High || len(bar.InputIDs) == 1 {
		bar.High = input.GetHigh()
	}
	if input.GetLow() < bar.Low || len(bar.InputIDs) == 1 {
		bar.Low = input.GetLow()
	}
	if start.After(bar.LastInput) || start.Equal(bar.LastInput) {
		bar.Close = input.GetClose()
		bar.LastInput = start
	}
	bar.Volume += input.GetVolume()
	bar.QuoteVolume += input.GetQuoteVolume()
	bar.TradeCount += input.GetTradeCount()
	if input.GetRevision() > bar.Revision {
		bar.Revision = input.GetRevision()
	}
	targetEnd := windowStart.Add(a.targetFrequency)
	if !bar.Closed && !end.Before(targetEnd) {
		bar.Closed = true
		a.closedAt[key] = end
		if bar.Revision == 0 {
			bar.Revision = 1
		} else {
			bar.Revision++
		}
	}
	return Result{Bar: cloneBar(bar)}, nil
}

// ApplyTick maps one exchange transaction into the configured input interval
// and reuses the same event-time/window logic as closed input bars. A later
// tick in the target window closes the bar when its input interval reaches
// the target boundary; sparse symbols remain pending until a later tick acts
// as the event-time watermark.
func (a *Aggregator) ApplyTick(eventID, spaceID string, input *marketpb.Tick) (Result, error) {
	if a == nil || input == nil || input.GetTradeTime() == nil {
		return Result{}, fmt.Errorf("aggregator or tick is nil")
	}
	tradeTime := input.GetTradeTime().AsTime().UTC()
	if eventID == "" || spaceID == "" || input.GetSymbol() == "" || input.GetPrice() <= 0 || input.GetQuantity() <= 0 {
		return Result{}, fmt.Errorf("tick has incomplete identity or values")
	}
	start := tradeTime.Truncate(a.inputFrequency)
	return a.Apply(eventID, spaceID, &marketpb.KlineClosed{
		Exchange: input.GetExchange(), Symbol: input.GetSymbol(), Frequency: formatFrequency(a.inputFrequency),
		WindowStart: timestamppb.New(start), WindowEnd: timestamppb.New(start.Add(a.inputFrequency)),
		Open: input.GetPrice(), High: input.GetPrice(), Low: input.GetPrice(), Close: input.GetPrice(),
		Volume: input.GetQuantity(), QuoteVolume: input.GetPrice() * input.GetQuantity(), TradeCount: 1,
	})
}

func (a *Aggregator) Snapshot() []Bar {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Bar, 0, len(a.windows))
	for _, bar := range a.windows {
		out = append(out, cloneBar(bar))
	}
	return out
}

func (a *Aggregator) Export() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := Snapshot{Windows: make([]BarSnapshot, 0, len(a.windows))}
	for key, bar := range a.windows {
		out.Windows = append(out.Windows, BarSnapshot{Bar: cloneBar(bar), ClosedAt: a.closedAt[key]})
	}
	return out
}

func (a *Aggregator) Restore(snapshot Snapshot) error {
	if a == nil {
		return fmt.Errorf("aggregator is nil")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, item := range snapshot.Windows {
		if item.Bar.Key.SpaceID == "" || item.Bar.Key.Subject == "" || item.Bar.Key.Start.IsZero() {
			return fmt.Errorf("snapshot contains incomplete bar identity")
		}
		bar := item.Bar
		bar.InputIDs = cloneIDs(bar.InputIDs)
		a.windows[bar.Key] = &bar
		if !item.ClosedAt.IsZero() {
			a.closedAt[bar.Key] = item.ClosedAt.UTC()
		}
	}
	return nil
}

func cloneBar(in *Bar) Bar {
	out := *in
	out.InputIDs = cloneIDs(in.InputIDs)
	return out
}

func cloneIDs(ids map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for id := range ids {
		out[id] = struct{}{}
	}
	return out
}

func formatFrequency(value time.Duration) string {
	if value%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(value/(24*time.Hour)))
	}
	if value%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(value/time.Hour))
	}
	return fmt.Sprintf("%dm", int(value/time.Minute))
}
