package notification

import "context"

type Sender interface {
	Send(context.Context, Message) error
}
