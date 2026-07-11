package order

type State string

const (
	Draft             State = "DRAFT"
	Ready             State = "READY"
	Submitting        State = "SUBMITTING"
	SubmitUnknown     State = "SUBMIT_UNKNOWN"
	Open              State = "OPEN"
	PartiallyFilled   State = "PARTIALLY_FILLED"
	Canceling         State = "CANCELING"
	Filled            State = "FILLED"
	Canceled          State = "CANCELED"
	PartiallyCanceled State = "PARTIALLY_CANCELED"
	Rejected          State = "REJECTED"
	Expired           State = "EXPIRED"
)

func (s State) Terminal() bool {
	return s == Filled || s == Canceled || s == PartiallyCanceled || s == Rejected || s == Expired
}
