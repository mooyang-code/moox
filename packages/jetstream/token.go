package jetstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/nats-io/nats.go"
)

const maxDeliveryTokenSize = 4096

type deliveryToken struct {
	Stream     string `json:"stream"`
	Consumer   string `json:"consumer"`
	AckSubject string `json:"ack_subject"`
}

func encodeDeliveryToken(stream, consumer, ackSubject string) (string, error) {
	if err := validateAckSubject(stream, consumer, ackSubject); err != nil {
		return "", err
	}
	raw, err := json.Marshal(deliveryToken{Stream: stream, Consumer: consumer, AckSubject: ackSubject})
	if err != nil {
		return "", fmt.Errorf("encode delivery token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if len(token) > maxDeliveryTokenSize {
		return "", fmt.Errorf("delivery token exceeds %d bytes", maxDeliveryTokenSize)
	}
	return token, nil
}

func decodeDeliveryToken(raw string) (deliveryToken, error) {
	if len(raw) == 0 || len(raw) > maxDeliveryTokenSize {
		return deliveryToken{}, fmt.Errorf("invalid delivery token length")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return deliveryToken{}, fmt.Errorf("invalid delivery token encoding: %w", err)
	}
	var token deliveryToken
	if err := json.Unmarshal(decoded, &token); err != nil {
		return deliveryToken{}, fmt.Errorf("invalid delivery token payload: %w", err)
	}
	if err := validateAckSubject(token.Stream, token.Consumer, token.AckSubject); err != nil {
		return deliveryToken{}, err
	}
	return token, nil
}

func validateAckSubject(stream, consumer, subject string) error {
	if strings.TrimSpace(stream) == "" || strings.TrimSpace(consumer) == "" || strings.TrimSpace(subject) == "" {
		return fmt.Errorf("delivery token requires stream, consumer, and ack subject")
	}
	if strings.ContainsAny(stream+consumer, " \t\r\n") || strings.ContainsAny(subject, " \t\r\n") {
		return fmt.Errorf("delivery token contains whitespace")
	}
	for _, r := range subject {
		if unicode.IsControl(r) {
			return fmt.Errorf("delivery token contains control character")
		}
	}
	prefix := "$JS.ACK." + stream + "." + consumer + "."
	if !strings.HasPrefix(subject, prefix) {
		return fmt.Errorf("delivery token ack subject does not match stream/consumer")
	}
	return nil
}

func (c *Client) sendToken(ctx context.Context, raw string, payload []byte, request bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.alive(); err != nil {
		return fmt.Errorf("%w: %w", ErrConnection, err)
	}
	token, err := decodeDeliveryToken(raw)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDelivery, err)
	}
	msg := &nats.Msg{Subject: token.AckSubject, Data: payload}
	timeout := time.Second
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) > 0 && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	if request {
		inbox := nats.NewInbox()
		msg.Reply = inbox
		sub, err := c.nc.SubscribeSync(inbox)
		if err != nil {
			return err
		}
		defer sub.Unsubscribe()
		if err := c.nc.PublishMsg(msg); err != nil {
			return err
		}
		if err := c.nc.FlushTimeout(timeout); err != nil {
			return err
		}
		if _, err := sub.NextMsg(timeout); err != nil {
			return err
		}
		return nil
	}
	if err := c.nc.PublishMsg(msg); err != nil {
		return err
	}
	return c.nc.FlushTimeout(timeout)
}

func (c *Client) AckToken(ctx context.Context, token string) error {
	return c.sendToken(ctx, token, []byte("+ACK"), true)
}

func (c *Client) NakToken(ctx context.Context, token string, delay time.Duration) error {
	if delay <= 0 {
		return c.sendToken(ctx, token, []byte("-NAK"), false)
	}
	payload, _ := json.Marshal(struct {
		Delay time.Duration `json:"delay"`
	}{Delay: delay})
	return c.sendToken(ctx, token, append([]byte("-NAK "), payload...), false)
}

func (c *Client) InProgressToken(ctx context.Context, token string) error {
	return c.sendToken(ctx, token, []byte("+WPI"), false)
}

func (c *Client) TermToken(ctx context.Context, token string) error {
	return c.sendToken(ctx, token, []byte("+TERM"), false)
}
