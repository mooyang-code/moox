package sina

import (
	"bytes"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// DailyRow is the unadjusted OHLCV row produced by Sina's K2 history codec.
// The codec is shared by A-share, Hong Kong and US daily endpoints.
type DailyRow struct {
	Date   string
	Open   string
	High   string
	Low    string
	Close  string
	Volume string
	Amount string
}

// ParseDailyPayload extracts Sina's quoted K2 payload and decodes its custom
// bit stream. It intentionally supports only K2: other historical codecs must
// not be mistaken for valid OHLCV data.
func ParseDailyPayload(raw []byte) ([]DailyRow, error) {
	encoded, err := extractEncodedPayload(raw)
	if err != nil {
		return nil, err
	}
	return decodeK2(encoded)
}

func extractEncodedPayload(raw []byte) (string, error) {
	raw = bytes.TrimSpace(raw)
	equal := bytes.IndexByte(raw, '=')
	if equal < 0 {
		return "", fmt.Errorf("Sina daily payload has no assignment")
	}
	quote := bytes.IndexByte(raw[equal+1:], '"')
	if quote < 0 {
		return "", fmt.Errorf("Sina daily payload has no quoted codec data")
	}
	start := equal + 1 + quote + 1
	var builder strings.Builder
	for index := start; index < len(raw); index++ {
		if raw[index] == '"' {
			value := builder.String()
			if !strings.HasPrefix(value, "K2/") {
				return "", fmt.Errorf("unsupported Sina daily codec %q", codecPrefix(value))
			}
			return value, nil
		}
		if raw[index] == '\\' {
			if index+1 >= len(raw) {
				return "", fmt.Errorf("Sina daily payload has an incomplete escape")
			}
			index++
			switch raw[index] {
			case '\\', '"', '/':
				builder.WriteByte(raw[index])
			case 'n':
				builder.WriteByte('\n')
			case 'r':
				builder.WriteByte('\r')
			case 't':
				builder.WriteByte('\t')
			default:
				return "", fmt.Errorf("unsupported Sina daily escape \\\\%c", raw[index])
			}
			continue
		}
		builder.WriteByte(raw[index])
	}
	return "", fmt.Errorf("Sina daily payload has an unterminated quoted codec data")
}

func codecPrefix(value string) string {
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

type bitReader struct {
	values []uint8
	index  int
	bit    uint
}

func newBitReader(encoded string) (*bitReader, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	values := make([]uint8, len(encoded))
	for index := range encoded {
		value := strings.IndexByte(alphabet, encoded[index])
		if value < 0 {
			return nil, fmt.Errorf("Sina K2 payload contains invalid base64 character at %d", index)
		}
		values[index] = uint8(value)
	}
	return &bitReader{values: values}, nil
}

func (reader *bitReader) exhausted() bool { return reader.index >= len(reader.values) }

func (reader *bitReader) bitValue() (bool, error) {
	if reader.exhausted() {
		return false, ioEOF{}
	}
	value := reader.values[reader.index]&(1<<reader.bit) != 0
	reader.bit++
	if reader.bit == 6 {
		reader.bit = 0
		reader.index++
	}
	return value, nil
}

type ioEOF struct{}

func (ioEOF) Error() string { return "Sina K2 bit stream ended" }

func (reader *bitReader) read(widths []int, signed []bool, split []bool) ([]int64, error) {
	values := make([]int64, len(widths))
	for index, width := range widths {
		if width <= 0 {
			continue
		}
		if width <= 30 {
			value, err := reader.readBits(width)
			if err != nil {
				return nil, err
			}
			if index < len(signed) && signed[index] && value >= 1<<uint(width-1) {
				value -= 1 << uint(width)
			}
			values[index] = value
			continue
		}
		parts, err := reader.read([]int{30, width - 30}, []bool{false, index < len(signed) && signed[index]}, nil)
		if err != nil {
			return nil, err
		}
		if index >= len(split) || !split[index] {
			values[index] = parts[0] + parts[1]*(1<<30)
		} else {
			values[index] = parts[0]
		}
	}
	return values, nil
}

func (reader *bitReader) readBits(width int) (int64, error) {
	var value uint64
	var shift uint
	for width > 0 {
		available := int(6 - reader.bit)
		if available > width {
			available = width
		}
		if reader.exhausted() {
			return 0, ioEOF{}
		}
		mask := uint8((1 << uint(available)) - 1)
		value |= uint64((reader.values[reader.index]>>reader.bit)&mask) << shift
		reader.bit += uint(available)
		if reader.bit == 6 {
			reader.bit = 0
			reader.index++
		}
		shift += uint(available)
		width -= available
	}
	return int64(value), nil
}

func (reader *bitReader) unarySigned() (int64, error) {
	first, err := reader.bitValue()
	if err != nil {
		return 0, err
	}
	count := int64(1)
	for {
		continuation, err := reader.bitValue()
		if err != nil {
			return 0, err
		}
		if !continuation {
			if first {
				return count, nil
			}
			return -count, nil
		}
		count++
	}
}

type k2State struct {
	day                        int
	bAVP, bPH, bPHX, bSep      bool
	wd                         int64
	pP, pV, pA, pE, pT         int64
	lO, lH, lL, lC, lV, lA     int64
	lE, lT                     int64
	uO, uH, uL, uC, uP, uV, uA int64
}

func decimalExponent(value int64) (int, int) {
	if value == 0 {
		return 0, 0
	}
	if value < 0 {
		first, second := decimalExponent(-value)
		return -first, -second
	}
	quotient := value / 3
	remainder := value % 3
	first, second := int(quotient), int(quotient)
	if remainder != 0 {
		if remainder == 1 {
			first++
		} else {
			second++
		}
	}
	return first, second
}

func scaleFactor(from, to int64) *big.Rat {
	fromFirst, fromSecond := decimalExponent(from)
	toFirst, toSecond := decimalExponent(to)
	return scaleFactorPairs(fromFirst, fromSecond, toFirst, toSecond)
}

func scaleFactorPairs(fromFirst, fromSecond, toFirst, toSecond int) *big.Rat {
	first, second := toFirst-fromFirst, toSecond-fromSecond
	factor := new(big.Rat).SetInt64(1)
	if first < second {
		factor.Mul(factor, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(5), big.NewInt(int64(second-first)), nil)))
		second = first
	}
	if second < first {
		factor.Mul(factor, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(2), big.NewInt(int64(first-second)), nil)))
		first = second
	}
	if first > 0 {
		factor.Mul(factor, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(first)), nil)))
	} else if first < 0 {
		factor.Quo(factor, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-first)), nil)))
	}
	return factor
}

func roundHalfEven(value *big.Rat) (int64, error) {
	numerator := new(big.Int).Set(value.Num())
	denominator := new(big.Int).Set(value.Denom())
	negative := numerator.Sign() < 0
	if negative {
		numerator.Abs(numerator)
	}
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	twiceRemainder := new(big.Int).Lsh(new(big.Int).Set(remainder), 1)
	if twiceRemainder.Cmp(denominator) > 0 || (twiceRemainder.Cmp(denominator) == 0 && quotient.Bit(0) == 1) {
		quotient.Add(quotient, big.NewInt(1))
	}
	if negative {
		quotient.Neg(quotient)
	}
	if !quotient.IsInt64() {
		return 0, fmt.Errorf("Sina K2 scaled integer overflows int64")
	}
	return quotient.Int64(), nil
}

func scaleInteger(value, from, to int64, round bool) (int64, error) {
	factor := scaleFactor(from, to)
	rational := new(big.Rat).Mul(new(big.Rat).SetInt64(value), factor)
	if round {
		return roundHalfEven(rational)
	}
	if !rational.IsInt() {
		return 0, fmt.Errorf("Sina K2 non-integral conversion requested")
	}
	if !rational.Num().IsInt64() {
		return 0, fmt.Errorf("Sina K2 scaled integer overflows int64")
	}
	return rational.Num().Int64(), nil
}

func scaleIntegerPairs(value int64, fromFirst, fromSecond, toFirst, toSecond int, round bool) (int64, error) {
	factor := scaleFactorPairs(fromFirst, fromSecond, toFirst, toSecond)
	rational := new(big.Rat).Mul(new(big.Rat).SetInt64(value), factor)
	if round {
		return roundHalfEven(rational)
	}
	if !rational.IsInt() || !rational.Num().IsInt64() {
		return 0, fmt.Errorf("Sina K2 non-integral conversion requested")
	}
	return rational.Num().Int64(), nil
}

func formatScaledInteger(value, from int64) string {
	rational := new(big.Rat).Mul(new(big.Rat).SetInt64(value), scaleFactor(from, 0))
	negative := rational.Sign() < 0
	if negative {
		rational.Abs(rational)
	}
	integer, remainder := new(big.Int), new(big.Int)
	integer.QuoRem(rational.Num(), rational.Denom(), remainder)
	result := integer.String()
	if remainder.Sign() != 0 {
		result += "."
		for remainder.Sign() != 0 {
			remainder.Mul(remainder, big.NewInt(10))
			digit, next := new(big.Int), new(big.Int)
			digit.QuoRem(remainder, rational.Denom(), next)
			result += digit.String()
			remainder = next
		}
	}
	if negative && result != "0" {
		return "-" + result
	}
	return result
}

func k2Value(value int64, from, to int64, round bool) (string, error) {
	// P(value, precision) in the reference codec converts the stored integer
	// from that precision to the decimal number (target precision zero).
	if round {
		scaled, err := scaleInteger(value, from, to, true)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(scaled, 10), nil
	}
	return formatScaledInteger(value, from), nil
}

func decodeK2(encoded string) ([]DailyRow, error) {
	reader, err := newBitReader(encoded)
	if err != nil {
		return nil, err
	}
	header, err := reader.read([]int{12, 6}, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("Sina K2 header: %w", err)
	}
	if header[0] != 3466 {
		return nil, fmt.Errorf("Sina daily codec header %d is not K2", header[0])
	}
	if 63^header[1] != 0 {
		return nil, fmt.Errorf("Sina K2 variant %d is unsupported", 63^header[1])
	}
	state := k2State{bAVP: true, pP: 6, lO: 3, lH: 3, lL: 3, lC: 3, lV: 5, lA: 5, lE: 3, wd: 62}
	result := make([]DailyRow, 0, 1024)
	for !reader.exhausted() {
		row, ok, err := reader.readK2Row(&state)
		if err != nil {
			return nil, fmt.Errorf("Sina K2 row %d: %w", len(result), err)
		}
		if !ok {
			break
		}
		result = append(result, row)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("Sina K2 payload contains no rows")
	}
	return result, nil
}

func (reader *bitReader) readK2Row(state *k2State) (DailyRow, bool, error) {
	change, err := reader.bitValue()
	if err != nil {
		return DailyRow{}, false, nil
	}
	rowChangeCount := 0
	rowDelta := 1
	var previousAmountAvailable bool
	var previousAmount float64
	if change {
		second, err := reader.bitValue()
		if err != nil {
			return DailyRow{}, false, err
		}
		if !second {
			if err := reader.readSimpleK2Update(state, &rowDelta); err != nil {
				return DailyRow{}, false, err
			}
		} else {
			third, err := reader.bitValue()
			if err != nil {
				return DailyRow{}, false, err
			}
			if third {
				rowChangeCount++
				previousAmountAvailable = state.bAVP
				if err := reader.readK2HeaderUpdate(state); err != nil {
					return DailyRow{}, false, err
				}
				if err := reader.readK2PrecisionUpdate(state); err != nil {
					return DailyRow{}, false, err
				}
				if !state.bAVP && previousAmountAvailable {
					previousAmount = float64(state.uA)
				}
			}
			more, err := reader.bitValue()
			if err != nil {
				return DailyRow{}, false, err
			}
			if more {
				rowChangeCount++
				for index := 0; index < 7+boolInt(state.bPH)+boolInt(state.bPHX); index++ {
					changed, err := reader.bitValue()
					if err != nil {
						return DailyRow{}, false, err
					}
					if !changed {
						continue
					}
					if index == 6 {
						rowDelta, err = reader.readK2DateDelta(state)
						if err != nil {
							return DailyRow{}, false, err
						}
						continue
					}
					if err := reader.addK2Length(state, index); err != nil {
						return DailyRow{}, false, err
					}
				}
			}
			more, err = reader.bitValue()
			if err != nil {
				return DailyRow{}, false, err
			}
			if more {
				rowChangeCount++
				length := state.lO
				extra, err := reader.bitValue()
				if err != nil {
					return DailyRow{}, false, err
				}
				if extra {
					extraValue, err := reader.unarySigned()
					if err != nil {
						return DailyRow{}, false, err
					}
					length += extraValue
				}
				value, err := reader.read([]int{int(3 * length)}, []bool{true}, nil)
				if err != nil {
					return DailyRow{}, false, err
				}
				if state.bSep {
					state.uC += value[0]
				} else {
					state.uP += value[0]
				}
			}
			if rowChangeCount == 0 {
				return DailyRow{}, false, nil
			}
		}
	} else {
		// No adaptive update is encoded when the outer change bit is clear.
	}
	if previousAmountAvailable {
		state.uA = int64(previousAmount)
	}
	values := make([]int64, 0, 8)
	for index := 0; index < 6+boolInt(state.bPH)+boolInt(state.bPHX); index++ {
		field := "ohlcvaet"[index]
		signed := ((boolInt(state.bSep)*191 + boolInt(!state.bSep)*185) >> index & 1) != 0
		length := state.k2Length(field)
		value, err := reader.read([]int{int(3 * length)}, []bool{signed}, nil)
		if err != nil {
			return DailyRow{}, false, err
		}
		values = append(values, value[0])
	}
	date := state.k2Date(rowDelta)
	valueAt := func(field byte) int64 { return values[strings.IndexByte("ohlcvaet", field)] }
	var open, high, low, close int64
	if state.bSep {
		state.uO += valueAt('o')
		state.uH += valueAt('h')
		state.uL += valueAt('l')
		state.uC += valueAt('c')
		open, high, low, close = state.uO, state.uH, state.uL, state.uC
	} else {
		base := state.uP + valueAt('o')
		open, high, low, close = base, base+valueAt('h'), base-valueAt('l'), base+valueAt('c')
		state.uP = close
	}
	state.uV += valueAt('v')
	volume := state.uV
	amount := state.uA
	if state.bAVP {
		avg := float64(open+high+low+close) / 4
		if !state.bSep {
			avg = float64(open) + float64(valueAt('h')-valueAt('l')+valueAt('c'))/4
		}
		amountValue := math.Floor(avg*float64(state.uV) + 0.5)
		fromFirst, fromSecond := decimalExponent(state.pP)
		volumeFirst, volumeSecond := decimalExponent(state.pV)
		amountFirst, amountSecond := decimalExponent(state.pA)
		scaledAmount, err := scaleIntegerPairs(int64(amountValue), fromFirst+volumeFirst, fromSecond+volumeSecond, amountFirst, amountSecond, true)
		if err != nil {
			return DailyRow{}, false, fmt.Errorf("amount conversion: %w", err)
		}
		amount = scaledAmount + valueAt('a')
	} else {
		state.uA += valueAt('a')
		amount = state.uA
	}
	openValue, err := k2Value(open, state.pP, state.pP, false)
	if err != nil {
		return DailyRow{}, false, fmt.Errorf("open conversion: %w", err)
	}
	highValue, err := k2Value(high, state.pP, state.pP, false)
	if err != nil {
		return DailyRow{}, false, fmt.Errorf("high conversion: %w", err)
	}
	lowValue, err := k2Value(low, state.pP, state.pP, false)
	if err != nil {
		return DailyRow{}, false, fmt.Errorf("low conversion: %w", err)
	}
	closeValue, err := k2Value(close, state.pP, state.pP, false)
	if err != nil {
		return DailyRow{}, false, fmt.Errorf("close conversion: %w", err)
	}
	volumeValue, err := k2Value(volume, state.pV, state.pV, false)
	if err != nil {
		return DailyRow{}, false, fmt.Errorf("volume conversion: %w", err)
	}
	amountValueString, err := k2Value(amount, state.pA, state.pA, false)
	if err != nil {
		return DailyRow{}, false, fmt.Errorf("amount conversion: %w", err)
	}
	return DailyRow{
		Date: date.Format("2006-01-02"), Open: openValue, High: highValue, Low: lowValue, Close: closeValue,
		Volume: volumeValue, Amount: amountValueString,
	}, true, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (reader *bitReader) readSimpleK2Update(state *k2State, delta *int) error {
	first, err := reader.bitValue()
	if err != nil {
		return err
	}
	if !first {
		value, err := reader.read([]int{2}, nil, nil)
		if err != nil {
			return err
		}
		field := "ohlc"[value[0]]
		return reader.addK2Length(state, strings.IndexByte("ohlcvaet", byte(field)))
	}
	second, err := reader.bitValue()
	if err != nil {
		return err
	}
	if second {
		third, err := reader.bitValue()
		if err != nil {
			return err
		}
		if third {
			*delta, err = reader.readK2DateDelta(state)
			return err
		}
		return reader.addK2Length(state, 4)
	}
	if state.bPH {
		changed, err := reader.bitValue()
		if err != nil {
			return err
		}
		if changed {
			field := byte('e')
			if state.bPHX {
				changed, err = reader.bitValue()
				if err != nil {
					return err
				}
				if changed {
					field = 't'
				}
			}
			return reader.addK2Length(state, strings.IndexByte("ohlcvaet", field))
		}
	}
	return reader.addK2Length(state, 5)
}

func (reader *bitReader) readK2PrecisionUpdate(state *k2State) error {
	fields := "pvaet"
	for index := 0; index < 3+2*boolInt(state.bPH); index++ {
		changed, err := reader.bitValue()
		if err != nil {
			return err
		}
		if !changed {
			continue
		}
		field := fields[index]
		oldPrecision := state.k2Precision(field)
		increment, err := reader.unarySigned()
		if err != nil {
			return err
		}
		newPrecision := oldPrecision + increment
		state.setK2Precision(field, newPrecision)
		value := state.k2Unit(field)
		scaled, err := scaleInteger(value, oldPrecision, newPrecision, true)
		if err != nil {
			return fmt.Errorf("precision conversion for %c: %w", field, err)
		}
		state.setK2Unit(field, scaled)
		if state.bSep && index == 0 {
			for _, priceField := range []byte{'o', 'h', 'l', 'c'} {
				price := state.k2Unit(priceField)
				scaled, err := scaleInteger(price, oldPrecision, state.pP, true)
				if err != nil {
					return fmt.Errorf("price precision conversion for %c: %w", priceField, err)
				}
				state.setK2Unit(priceField, scaled)
			}
		}
	}
	if !state.bAVP {
		state.uA = 0
	}
	return nil
}

func (reader *bitReader) readK2HeaderUpdate(state *k2State) error {
	changed, err := reader.bitValue()
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	value, err := reader.bitValue()
	if err != nil {
		return err
	}
	state.bAVP = state.bAVP != value
	value, err = reader.bitValue()
	if err != nil {
		return err
	}
	state.bPH = state.bPH != value
	value, err = reader.bitValue()
	if err != nil {
		return err
	}
	state.bPHX = state.bPHX != value
	oldSeparate := state.bSep
	value, err = reader.bitValue()
	if err != nil {
		return err
	}
	state.bSep = state.bSep != value
	if changed, err := reader.bitValue(); err != nil {
		return err
	} else if changed {
		value, err := reader.read([]int{7}, nil, nil)
		if err != nil {
			return err
		}
		state.wd = value[0]
	}
	if oldSeparate != state.bSep {
		if oldSeparate {
			state.uP = state.uC
		} else {
			state.uO, state.uH, state.uL, state.uC = state.uP, state.uP, state.uP, state.uP
		}
	}
	return nil
}

func (reader *bitReader) addK2Length(state *k2State, index int) error {
	const fields = "ohlcva*et"
	if index < 0 || index >= len(fields) || fields[index] == '*' {
		return fmt.Errorf("invalid K2 adaptive field %d", index)
	}
	field := fields[index]
	value, err := reader.unarySigned()
	if err != nil {
		return err
	}
	state.setK2Length(field, state.k2Length(field)+value)
	return nil
}

func (state *k2State) k2Length(field byte) int64 {
	switch field {
	case 'o':
		return state.lO
	case 'h':
		return state.lH
	case 'l':
		return state.lL
	case 'c':
		return state.lC
	case 'v':
		return state.lV
	case 'a':
		return state.lA
	case 'e':
		return state.lE
	case 't':
		return state.lT
	default:
		return 0
	}
}

func (state *k2State) setK2Length(field byte, value int64) {
	switch field {
	case 'o':
		state.lO = value
	case 'h':
		state.lH = value
	case 'l':
		state.lL = value
	case 'c':
		state.lC = value
	case 'v':
		state.lV = value
	case 'a':
		state.lA = value
	case 'e':
		state.lE = value
	case 't':
		state.lT = value
	}
}

func (state *k2State) k2Precision(field byte) int64 {
	switch field {
	case 'p':
		return state.pP
	case 'v':
		return state.pV
	case 'a':
		return state.pA
	case 'e':
		return state.pE
	case 't':
		return state.pT
	default:
		return 0
	}
}

func (state *k2State) setK2Precision(field byte, value int64) {
	switch field {
	case 'p':
		state.pP = value
	case 'v':
		state.pV = value
	case 'a':
		state.pA = value
	case 'e':
		state.pE = value
	case 't':
		state.pT = value
	}
}

func (state *k2State) k2Unit(field byte) int64 {
	switch field {
	case 'o':
		return state.uO
	case 'h':
		return state.uH
	case 'l':
		return state.uL
	case 'c':
		return state.uC
	case 'p':
		return state.uP
	case 'v':
		return state.uV
	case 'a':
		return state.uA
	default:
		return 0
	}
}

func (state *k2State) setK2Unit(field byte, value int64) {
	switch field {
	case 'o':
		state.uO = value
	case 'h':
		state.uH = value
	case 'l':
		state.uL = value
	case 'c':
		state.uC = value
	case 'p':
		state.uP = value
	case 'v':
		state.uV = value
	case 'a':
		state.uA = value
	}
}

func (reader *bitReader) readK2DateDelta(state *k2State) (int, error) {
	value, err := reader.read([]int{3}, nil, nil)
	if err != nil {
		return 0, err
	}
	delta := int(value[0])
	if delta == 1 {
		value, err = reader.read([]int{18}, []bool{true}, nil)
		if err != nil {
			return 0, err
		}
		state.day = int(value[0])
		return 0, nil
	}
	if delta == 0 {
		value, err = reader.read([]int{6}, nil, nil)
		if err != nil {
			return 0, err
		}
		delta = int(value[0])
	}
	return delta, nil
}

func (state *k2State) k2Date(delta int) time.Time {
	for step := 0; step < delta; step++ {
		state.day++
		weekday := state.day % 7
		if weekday == 3 || weekday == 4 {
			state.day += 5 - weekday
		}
	}
	return time.UnixMilli(int64(7657+state.day) * 864e5).UTC()
}
