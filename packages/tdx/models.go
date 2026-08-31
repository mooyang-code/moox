package tdx

import "time"

// ProtocolVariant is deliberately explicit: ordinary TDX, classic extended
// TDX and MAC extended TDX use different framing and session setup.
type ProtocolVariant string

const (
	ProtocolNormal    ProtocolVariant = "tdx_normal"
	ProtocolExClassic ProtocolVariant = "tdx_ex_classic"
	ProtocolExMAC     ProtocolVariant = "tdx_ex_mac"
)

type Market uint16

const (
	MarketSZ Market = 0
	MarketSH Market = 1
	MarketBJ Market = 2
)

type KlineCategory uint16

const (
	Category5Min    KlineCategory = 0
	Category15Min   KlineCategory = 1
	Category30Min   KlineCategory = 2
	Category60Min   KlineCategory = 3
	CategoryDay     KlineCategory = 4
	CategoryWeek    KlineCategory = 5
	CategoryMonth   KlineCategory = 6
	Category1Min    KlineCategory = 7
	Category3Min    KlineCategory = 8
	CategoryYear    KlineCategory = 9
	CategorySeason  KlineCategory = 10
	CategoryYearAlt KlineCategory = 11
)

func (c KlineCategory) Intraday() bool {
	return c < CategoryDay || c == Category1Min || c == Category3Min
}

type Bar struct {
	Time     time.Time
	Open     float64
	High     float64
	Low      float64
	Close    float64
	Volume   float64
	Amount   float64
	Position uint32
	Trade    uint32
	Raw      []byte
}

type Security struct {
	Market       Market
	Code         string
	Name         string
	VolumeUnit   uint16
	DecimalPoint uint8
	PreClose     float64
	Raw          []byte
}

type ExtendedMarket struct {
	Market    uint8
	Category  uint8
	Name      string
	ShortName string
	Raw       []byte
}

type ExtendedSecurity struct {
	Category uint8
	Market   uint8
	Code     string
	Name     string
	Desc     string
	Raw      []byte
}

type Header struct {
	Magic     uint32
	Sequence  uint32
	Method    uint32
	ZipSize   uint16
	UnzipSize uint16
}

func (h Header) Compressed() bool { return h.ZipSize != h.UnzipSize }

type Request struct {
	Payload []byte
	// MACHeadFlag is used only by MAC extended frames. Zero selects 0x1c.
	MACHeadFlag byte
}
