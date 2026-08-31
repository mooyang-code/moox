package tdx

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

type ClientOptions struct {
	Host    string
	Port    int
	Variant ProtocolVariant
	Timeout time.Duration
	Dial    DialFunc
}

// Probe performs the smallest source-specific request that proves the
// endpoint speaks the selected protocol. A TCP connect alone is not enough.
func Probe(ctx context.Context, options ClientOptions) error {
	if options.Variant == "" {
		options.Variant = ProtocolNormal
	}
	client, err := NewClient(options)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.Connect(ctx); err != nil {
		return err
	}
	switch options.Variant {
	case ProtocolNormal:
		body, _, err := client.Execute(ctx, SecurityCountRequest(MarketSZ))
		if err != nil {
			return err
		}
		if len(body) < 2 {
			return errors.New("tdx: normal protocol probe returned no count")
		}
	case ProtocolExClassic:
		body, _, err := client.Execute(ctx, ExtendedMarketsRequest())
		if err != nil {
			return err
		}
		if len(body) < 2 {
			return errors.New("tdx: classic extended probe returned no market count")
		}
	case ProtocolExMAC:
		login, err := MACExLoginRequest()
		if err != nil {
			return err
		}
		body, _, err := client.Execute(ctx, login)
		if err != nil {
			return err
		}
		if len(body) < 2 {
			return errors.New("tdx: MAC extended probe login returned empty body")
		}
	default:
		return fmt.Errorf("tdx: unsupported probe variant %q", options.Variant)
	}
	return nil
}

type Client struct {
	host    string
	port    int
	variant ProtocolVariant
	timeout time.Duration
	dial    DialFunc

	mu   sync.Mutex
	conn net.Conn
}

func NewClient(options ClientOptions) (*Client, error) {
	if options.Host == "" {
		return nil, errors.New("tdx: host is required")
	}
	if options.Port <= 0 || options.Port > 65535 {
		return nil, fmt.Errorf("tdx: invalid port %d", options.Port)
	}
	if options.Variant == "" {
		options.Variant = ProtocolNormal
	}
	if options.Timeout <= 0 {
		options.Timeout = 15 * time.Second
	}
	if options.Dial == nil {
		options.Dial = (&net.Dialer{}).DialContext
	}
	return &Client{host: options.Host, port: options.Port, variant: options.Variant, timeout: options.Timeout, dial: options.Dial}, nil
}

func (c *Client) Connect(ctx context.Context) error {
	if c == nil {
		return errors.New("tdx: nil client")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(c.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	dialCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	conn, err := c.dial(dialCtx, "tcp", fmt.Sprintf("%s:%d", c.host, c.port))
	if err != nil {
		return fmt.Errorf("tdx: connect %s:%d: %w", c.host, c.port, err)
	}
	c.conn = conn
	if c.variant == ProtocolNormal {
		for i, setup := range setupCommands {
			if _, _, err := c.roundTripLocked(dialCtx, setup); err != nil {
				_ = conn.Close()
				c.conn = nil
				return fmt.Errorf("tdx: setup %d: %w", i+1, err)
			}
		}
	}
	return nil
}

func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

func (c *Client) Execute(ctx context.Context, request []byte) ([]byte, Header, error) {
	if len(request) == 0 {
		return nil, Header{}, errors.New("tdx: empty request")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil, Header{}, errors.New("tdx: client is not connected")
	}
	body, header, err := c.roundTripLocked(ctx, request)
	if err != nil {
		return nil, Header{}, err
	}
	return body, header, nil
}

func (c *Client) roundTripLocked(ctx context.Context, request []byte) ([]byte, Header, error) {
	fail := func(err error) ([]byte, Header, error) {
		// A partial frame leaves unread bytes (or a closed peer) on the
		// connection. Do not let a later request reuse that poisoned stream.
		if c.conn != nil {
			_ = c.conn.Close()
			c.conn = nil
		}
		return nil, Header{}, err
	}
	deadline := time.Now().Add(c.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return fail(fmt.Errorf("tdx: set deadline: %w", err))
	}
	if err := writeFull(c.conn, request); err != nil {
		return fail(fmt.Errorf("tdx: write request: %w", err))
	}
	headerBytes := make([]byte, HeaderSize)
	if _, err := io.ReadFull(c.conn, headerBytes); err != nil {
		return fail(fmt.Errorf("tdx: read frame header: %w", err))
	}
	header, err := ParseHeader(headerBytes)
	if err != nil {
		return fail(err)
	}
	if header.ZipSize == 0 && header.UnzipSize != 0 {
		return fail(fmt.Errorf("tdx: invalid frame body lengths zip=%d unzip=%d", header.ZipSize, header.UnzipSize))
	}
	rawBody := make([]byte, int(header.ZipSize))
	if _, err := io.ReadFull(c.conn, rawBody); err != nil {
		return fail(fmt.Errorf("tdx: read frame body: %w", err))
	}
	body, err := DecodeBody(header, rawBody)
	if err != nil {
		return fail(err)
	}
	return body, header, nil
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (c *Client) Variant() ProtocolVariant { return c.variant }
func (c *Client) Address() string          { return fmt.Sprintf("%s:%d", c.host, c.port) }

type NormalClient struct{ *Client }

func NewNormalClient(host string, port int, timeout time.Duration) (*NormalClient, error) {
	client, err := NewClient(ClientOptions{Host: host, Port: port, Timeout: timeout, Variant: ProtocolNormal})
	if err != nil {
		return nil, err
	}
	return &NormalClient{Client: client}, nil
}

func (c *NormalClient) SecurityBars(ctx context.Context, market Market, code string, category KlineCategory, start, count int, index bool) ([]Bar, error) {
	request, err := SecurityBarsRequest(market, code, category, start, count)
	if err != nil {
		return nil, err
	}
	body, _, err := c.Execute(ctx, request)
	if err != nil {
		return nil, err
	}
	return ParseSecurityBars(body, category, index)
}

func (c *NormalClient) SecurityList(ctx context.Context, market Market, start int) ([]Security, error) {
	request, err := SecurityListRequest(market, start)
	if err != nil {
		return nil, err
	}
	body, _, err := c.Execute(ctx, request)
	if err != nil {
		return nil, err
	}
	return ParseSecurityList(body, market)
}

func (c *NormalClient) SecurityCount(ctx context.Context, market Market) (int, error) {
	body, _, err := c.Execute(ctx, SecurityCountRequest(market))
	if err != nil {
		return 0, err
	}
	if len(body) < 2 {
		return 0, errors.New("tdx: security count response is truncated")
	}
	return int(uint16(body[0]) | uint16(body[1])<<8), nil
}

type ExtendedClient struct{ *Client }

func NewExtendedClient(host string, port int, variant ProtocolVariant, timeout time.Duration) (*ExtendedClient, error) {
	if variant != ProtocolExClassic && variant != ProtocolExMAC {
		return nil, fmt.Errorf("tdx: extended client requires extended variant, got %q", variant)
	}
	client, err := NewClient(ClientOptions{Host: host, Port: port, Timeout: timeout, Variant: variant})
	if err != nil {
		return nil, err
	}
	return &ExtendedClient{Client: client}, nil
}

func (c *ExtendedClient) Login(ctx context.Context) error {
	if c.variant != ProtocolExMAC {
		return nil
	}
	request, err := MACExLoginRequest()
	if err != nil {
		return err
	}
	body, _, err := c.Execute(ctx, request)
	if err != nil {
		return err
	}
	if len(body) < 2 {
		return errors.New("tdx: MAC EX login response is empty")
	}
	return nil
}

func (c *ExtendedClient) Markets(ctx context.Context) ([]ExtendedMarket, error) {
	if c.variant == ProtocolExMAC {
		return nil, errors.New("tdx: MAC extended market list is not the classic market command")
	}
	body, _, err := c.Execute(ctx, ExtendedMarketsRequest())
	if err != nil {
		return nil, err
	}
	return ParseExtendedMarkets(body)
}

func (c *ExtendedClient) InstrumentCount(ctx context.Context) (int, error) {
	body, _, err := c.Execute(ctx, ExtendedInstrumentCountRequest())
	if err != nil {
		return 0, err
	}
	if len(body) < 23 {
		return 0, errors.New("tdx: extended instrument count response is truncated")
	}
	return int(binary.LittleEndian.Uint32(body[19:23])), nil
}

func (c *ExtendedClient) InstrumentInfo(ctx context.Context, start, count int) ([]ExtendedSecurity, error) {
	request, err := ExtendedInstrumentInfoRequest(start, count)
	if err != nil {
		return nil, err
	}
	body, _, err := c.Execute(ctx, request)
	if err != nil {
		return nil, err
	}
	return ParseExtendedInstrumentInfo(body)
}

func (c *ExtendedClient) ConnectAndLogin(ctx context.Context) error {
	if err := c.Connect(ctx); err != nil {
		return err
	}
	if err := c.Login(ctx); err != nil {
		_ = c.Close()
		return err
	}
	return nil
}

func (c *ExtendedClient) Bars(ctx context.Context, market uint8, code string, category KlineCategory, start, count int) ([]Bar, error) {
	var request []byte
	var err error
	if c.variant == ProtocolExMAC {
		request, err = MACBarsRequest(uint16(market), code, int(category), 1, start, count, 0)
	} else {
		request, err = ExtendedBarsRequest(market, code, category, start, count)
	}
	if err != nil {
		return nil, err
	}
	body, _, err := c.Execute(ctx, request)
	if err != nil {
		return nil, err
	}
	if c.variant == ProtocolExMAC {
		return ParseMACBars(body, category)
	}
	return ParseExtendedBars(body, category)
}
