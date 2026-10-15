package pdu

import (
	"fmt"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-sms/encoding/tpdu"
)

// NewIENationalLanguageSingleShift creates IEI 0x24 (TS 23.040
// section 9.2.3.24.15). Zero means omission in GSM7Options, not a wire NLI.
func NewIENationalLanguageSingleShift(nli int) (InfoElement, error) {
	if nli == 0 {
		return InfoElement{}, fmt.Errorf("national language IE cannot carry reserved identifier 0")
	}
	if _, err := data.NewGSM7(data.GSM7Options{SingleShift: nli}); err != nil {
		return InfoElement{}, err
	}
	return InfoElement{ID: tpdu.IEINationalLanguageSingleShift, Data: []byte{byte(nli)}}, nil
}

// NewIENationalLanguageLockingShift creates IEI 0x25 (TS 23.040
// section 9.2.3.24.16). Spanish has no locking table (TS 23.038 Annex A.3.2).
func NewIENationalLanguageLockingShift(nli int) (InfoElement, error) {
	if nli == 0 {
		return InfoElement{}, fmt.Errorf("national language IE cannot carry reserved identifier 0")
	}
	if _, err := data.NewGSM7(data.GSM7Options{LockingShift: nli}); err != nil {
		return InfoElement{}, err
	}
	return InfoElement{ID: tpdu.IEINationalLanguageLockingShift, Data: []byte{byte(nli)}}, nil
}

func gsm7Options(enc data.Encoding) (data.GSM7Options, bool) {
	if custom, ok := enc.(*data.CustomEncoding); ok {
		base, ok := custom.BaseEncoding().(data.Encoding)
		if !ok {
			return data.GSM7Options{}, false
		}
		return gsm7Options(base)
	}
	if configured, ok := enc.(interface{ GSM7Options() data.GSM7Options }); ok {
		return configured.GSM7Options(), true
	}
	if enc == data.GSM7BITPACKED {
		// The legacy packed path borrows the selected alphabet and NLI tables,
		// but owns packing itself. Resolve once so self-replacement terminates.
		opts := data.GSM7Options{Packed: true}
		alphabet, err := data.UnpackedGSM7(data.GSM7BIT)
		if err == nil {
			if configured, ok := alphabet.(interface{ GSM7Options() data.GSM7Options }); ok {
				opts = configured.GSM7Options()
				opts.Packed = true
			}
		}
		return opts, true
	}
	return data.GSM7Options{}, enc == nil || enc == data.GSM7BIT
}

// Merge the explicit sender choice without changing caller-owned IE storage.
// TS 23.040 sections 9.2.3.24.15–16 require the selected IEs on every segment.
func nationalLanguageHeader(enc data.Encoding, udh UDH) (UDH, error) {
	opts, gsm := gsm7Options(enc)
	if !gsm {
		return udh, nil
	}
	seenSingle, seenLocking := false, false
	for _, ie := range udh {
		var want int
		switch ie.ID {
		case tpdu.IEINationalLanguageSingleShift:
			want, seenSingle = opts.SingleShift, true
		case tpdu.IEINationalLanguageLockingShift:
			want, seenLocking = opts.LockingShift, true
		default:
			continue
		}
		if want == 0 || len(ie.Data) != 1 || int(ie.Data[0]) != want {
			return nil, fmt.Errorf("national language IE 0x%02x conflicts with the selected GSM7 tables", ie.ID)
		}
	}
	if (opts.SingleShift == 0 || seenSingle) && (opts.LockingShift == 0 || seenLocking) {
		return udh, nil
	}
	result := append(UDH(nil), udh...)
	if opts.SingleShift != 0 && !seenSingle {
		ie, err := NewIENationalLanguageSingleShift(opts.SingleShift)
		if err != nil {
			return nil, err
		}
		result = append(result, ie)
	}
	if opts.LockingShift != 0 && !seenLocking {
		ie, err := NewIENationalLanguageLockingShift(opts.LockingShift)
		if err != nil {
			return nil, err
		}
		result = append(result, ie)
	}
	return result, nil
}

func (u UDH) hasNationalLanguage() bool {
	for _, ie := range u {
		if ie.ID == tpdu.IEINationalLanguageSingleShift || ie.ID == tpdu.IEINationalLanguageLockingShift {
			return true
		}
	}
	return false
}

// Let go-sms own last-applicable-IE selection and receive fallback, including
// reserved identifiers and Spanish locking (TS 23.038 sections 6.2.1.2.4–5;
// TS 23.040 sections 9.2.3.24.15–16).
func decodeGSM7WithUDH(septets []byte, udh UDH) (string, error) {
	text, err := tpdu.DecodeUserData(septets, udh.smsHeader(), tpdu.Alpha7Bit, tpdu.WithAllCharsets)
	return string(text), err
}

func unpackedGSMCodec(enc data.Encoding) (data.Encoding, error) {
	if custom, ok := enc.(*data.CustomEncoding); ok {
		if base, ok := custom.BaseEncoding().(data.Encoding); ok {
			return unpackedGSMCodec(base)
		}
	}
	// Legacy PDU setters share their caller-selected alphabet with the public
	// fill/count decoder. Standalone packed codecs remain independent of it.
	if enc == data.GSM7BITPACKED {
		return data.UnpackedGSM7(data.GSM7BIT)
	}
	return data.UnpackedGSM7(enc)
}

// GSM segmentation preserves the original septets and the segment's NLI IEs;
// it does not need to choose tables again or re-encode received text.
func splitGSMSeptets(septets []byte, limit int) ([][]byte, error) {
	var parts [][]byte
	for start := 0; start < len(septets); {
		end := min(start+limit, len(septets))
		if endsWithGSMEscape(septets[start:end]) {
			end--
		}
		if end == start {
			return nil, fmt.Errorf("split limit cannot fit a GSM character")
		}
		parts = append(parts, append([]byte(nil), septets[start:end]...))
		start = end
	}
	return parts, nil
}

// ESC ESC is a complete pair too. Only an odd run of trailing escapes
// leaves an introducer without its following septet (TS 23.040 9.2.3.24.1).
func endsWithGSMEscape(septets []byte) bool {
	escaped := false
	for i := len(septets) - 1; i >= 0 && septets[i] == 0x1b; i-- {
		escaped = !escaped
	}
	return escaped
}
