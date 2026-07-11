package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"sort"
)

var ErrInvalidPlan = errors.New("trade: invalid execution plan")

type Plan struct {
	ID                                  shared.ExecutionPlanID
	OrderID                             shared.OrderID
	Algorithm                           AlgorithmDescriptor
	Config                              map[string]string
	SnapshotID, RulesVersion, InputHash string
	ExpectedQuantity                    shared.Decimal
	Slices                              []Slice
	Version                             uint64
}

func NewPlan(id shared.ExecutionPlanID, oid shared.OrderID, a AlgorithmDescriptor, cfg map[string]string, snapshot, rules string, expected shared.Decimal, drafts []SliceDraft) (Plan, error) {
	p := Plan{ID: id, OrderID: oid, Algorithm: a, Config: cfg, SnapshotID: snapshot, RulesVersion: rules, ExpectedQuantity: expected, Version: 1}
	for _, d := range drafts {
		p.Slices = append(p.Slices, Slice{ID: shared.ExecutionSliceID(string(id) + "-" + itoa(d.Sequence)), Sequence: d.Sequence, Quantity: d.Quantity, FilledQuantity: shared.Zero(), State: SlicePlanned, DependsOn: d.DependsOn})
	}
	if err := p.Validate(); err != nil {
		return Plan{}, err
	}
	p.InputHash = p.Hash()
	return p, nil
}
func (p Plan) Validate() error {
	if p.ID == "" || p.OrderID == "" || p.Algorithm.Name == "" || p.Algorithm.Version == "" || len(p.Slices) == 0 {
		return ErrInvalidPlan
	}
	seen := map[int]bool{}
	total := shared.Zero()
	for _, s := range p.Slices {
		if s.Sequence <= 0 || seen[s.Sequence] || s.Quantity.Cmp(shared.Zero()) <= 0 {
			return ErrInvalidPlan
		}
		for _, d := range s.DependsOn {
			if d >= s.Sequence || !seen[d] {
				return ErrInvalidPlan
			}
		}
		seen[s.Sequence] = true
		total = total.Add(s.Quantity)
	}
	if total.IsZero() || total.Cmp(p.ExpectedQuantity) != 0 {
		return ErrInvalidPlan
	}
	return nil
}
func (p Plan) Hash() string {
	cfg := make([][2]string, 0, len(p.Config))
	for k, v := range p.Config {
		cfg = append(cfg, [2]string{k, v})
	}
	sort.Slice(cfg, func(i, j int) bool { return cfg[i][0] < cfg[j][0] })
	v := struct {
		A        AlgorithmDescriptor
		C        [][2]string
		S, R     string
		Expected string
		Slices   []Slice
	}{p.Algorithm, cfg, p.SnapshotID, p.RulesVersion, p.ExpectedQuantity.String(), p.Slices}
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 8)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
