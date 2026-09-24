package orchestrator

// defaultTierOrder is the escalation order when none is configured.
var defaultTierOrder = []string{"junior", "middle", "senior", "lead"}

// Dispatcher chooses and escalates role tiers. It is intentionally simple: the
// supervisor may delegate with tier "auto" (pick the lowest tier) and the
// dispatcher escalates on critic feedback.
type Dispatcher struct {
	Order []string
}

// NewDispatcher creates a dispatcher with the given escalation order.
func NewDispatcher(order ...string) *Dispatcher {
	if len(order) == 0 {
		order = defaultTierOrder
	}
	return &Dispatcher{Order: order}
}

// Rank returns the escalation rank of a tier (higher is stronger). Unknown
// tiers rank 0.
func (d *Dispatcher) Rank(tier string) int {
	for i, t := range d.Order {
		if t == tier {
			return i + 1
		}
	}
	return 0
}

// Lowest returns the weakest tier in the order.
func (d *Dispatcher) Lowest() string {
	if len(d.Order) == 0 {
		return ""
	}
	return d.Order[0]
}

// Escalate returns the next stronger tier, or the same tier at the top.
func (d *Dispatcher) Escalate(tier string) string {
	r := d.Rank(tier)
	if r == 0 {
		return d.Lowest()
	}
	if r >= len(d.Order) {
		return d.Order[len(d.Order)-1]
	}
	return d.Order[r]
}

// AtLeast returns the stronger of two tiers.
func (d *Dispatcher) AtLeast(a, b string) string {
	if d.Rank(a) >= d.Rank(b) {
		return a
	}
	return b
}
