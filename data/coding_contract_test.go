package data_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-sms/encoding/gsm7"
	"github.com/gomaja/go-sms/encoding/ucs2"
)

func TestPackedCodecIndependentOfGSM7Variable(t *testing.T) {
	saved := data.GSM7BIT
	t.Cleanup(func() { data.GSM7BIT = saved })
	data.GSM7BIT = data.ASCII
	assertPackedAlphabet(t, "all")

	// The public fill/count helper retains its caller-selected alphabet.
	text, err := data.DecodeGSM7Packed([]byte{0}, 0, 1)
	if err != nil || text != "\x00" {
		t.Errorf("public helper = %q, %v; want ASCII NUL", text, err)
	}
}

func assertPackedAlphabet(t *testing.T, operation string) {
	t.Helper()
	if operation == "encode" || operation == "all" {
		encoded, err := data.GSM7BITPACKED.Encode("@")
		if err != nil || !bytes.Equal(encoded, []byte{0}) {
			t.Errorf("packed Encode(@) = %x, %v; want 00", encoded, err)
		}
	}
	if operation == "decode" || operation == "all" {
		text, err := data.GSM7BITPACKED.Decode([]byte{0})
		if err != nil || text != "@" {
			t.Errorf("packed Decode(00) = %q, %v; want @", text, err)
		}
	}
	if operation == "split" || operation == "all" {
		segments, err := data.GSM7BITPACKED.(data.Splitter).EncodeSplit("@", 1)
		if err != nil || len(segments) != 1 || !bytes.Equal(segments[0], []byte{0}) {
			t.Errorf("packed EncodeSplit(@) = %x, %v; want [00]", segments, err)
		}
	}
}

func TestPackedCodecSelfReassignment(t *testing.T) {
	if operation := os.Getenv("SMPP_PACKED_REASSIGNMENT"); operation != "" {
		// Bound a regression's stack use in the child, never in the suite.
		debug.SetMaxStack(64 << 10)
		data.GSM7BIT = data.GSM7BITPACKED
		assertPackedAlphabet(t, operation)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"encode", "decode", "split"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPackedCodecSelfReassignment$")
			cmd.Env = append(os.Environ(), "SMPP_PACKED_REASSIGNMENT="+operation)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("reassignment child: %v\n%s", err, output[:min(len(output), 1600)])
			}
		})
	}
}

func TestCodingEmptyEncodeIsNonNil(t *testing.T) {
	for name, enc := range map[string]data.Encoding{
		"GSM7": data.GSM7BIT, "packed": data.GSM7BITPACKED,
		"UTF16": data.UCS2, "strict": data.UCS2Strict,
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := enc.Encode("")
			if err != nil || encoded == nil || len(encoded) != 0 {
				t.Errorf("Encode(empty): nil=%v, len=%d, error=%v; want non-nil empty", encoded == nil, len(encoded), err)
			}
		})
	}
}

// Hiding Splitter exercises CustomEncoding's EncDec-only fallback.
type encDecOnly struct{ data.EncDec }

func TestShouldSplitEncodingContract(t *testing.T) {
	for name, tc := range map[string]struct {
		enc    data.Encoding
		octets uint
		astral bool
	}{
		"GSM7":     {data.GSM7BIT, 8, false},
		"packed":   {data.GSM7BITPACKED, 7, false},
		"UTF16":    {data.UCS2, 16, true},
		"strict":   {data.UCS2Strict, 16, false},
		"ASCII":    {data.ASCII, 8, false},
		"Latin1":   {data.LATIN1, 8, false},
		"Cyrillic": {data.CYRILLIC, 8, false},
		"Hebrew":   {data.HEBREW, 8, false},
	} {
		for wrapper, enc := range map[string]data.Encoding{
			"direct": tc.enc,
			"custom": data.NewCustomEncoding(0x10, tc.enc),
			"encdec": data.NewCustomEncoding(0x10, encDecOnly{tc.enc}),
		} {
			t.Run(name+"/"+wrapper, func(t *testing.T) {
				splitter := enc.(data.Splitter)
				for _, limit := range []uint{0, tc.octets - 1, tc.octets, tc.octets + 1} {
					if got := splitter.ShouldSplit("AAAAAAA@", limit); got != (tc.octets > limit) {
						t.Errorf("valid text, limit %d: ShouldSplit=%v", limit, got)
					}
				}
				if splitter.ShouldSplit("", 0) {
					t.Error("empty text must not split")
				}
				prefix := strings.Repeat("A", 161)
				for _, input := range []string{prefix + "\xff", prefix + "😀"} {
					_, encodeErr := enc.Encode(input)
					if tc.astral && strings.HasSuffix(input, "😀") {
						if encodeErr != nil || !splitter.ShouldSplit(input, 140) {
							t.Errorf("UTF16 supplementary input must encode and exceed limit: %v", encodeErr)
						}
						continue
					}
					if encodeErr == nil {
						t.Fatal("fixture must be unencodable")
					}
					for _, limit := range []uint{0, 140, 1000} {
						if splitter.ShouldSplit(input, limit) {
							t.Errorf("unencodable text, limit %d: ShouldSplit=true", limit)
						}
					}
					// The caller can now use the single-part encoder and retain
					// the error's offset in the complete source message.
					switch name {
					case "GSM7", "packed":
						var invalid gsm7.ErrInvalidUTF8
						var unsupported gsm7.ErrUnencodable
						if strings.HasSuffix(input, "\xff") {
							if !errors.As(encodeErr, &invalid) || invalid.Offset != len(prefix) {
								t.Errorf("whole-message UTF8 error: %v", encodeErr)
							}
						} else if !errors.As(encodeErr, &unsupported) || unsupported.Offset != len(prefix) {
							t.Errorf("whole-message alphabet error: %v", encodeErr)
						}
					case "strict":
						if strings.HasSuffix(input, "😀") {
							var unsupported ucs2.ErrUnencodable
							if !errors.As(encodeErr, &unsupported) || unsupported.Index != len(prefix) {
								t.Errorf("whole-message strict error: %v", encodeErr)
							}
						}
					}
				}
			})
		}
	}
}

type partialErrorCodec struct{}

func (partialErrorCodec) Encode(string) ([]byte, error) {
	return []byte("partial"), errors.New("cannot encode remainder")
}

func (partialErrorCodec) Decode([]byte) (string, error) { return "", nil }

func TestCustomShouldSplitIgnoresPartialOutputOnError(t *testing.T) {
	enc := data.NewCustomEncoding(0x10, partialErrorCodec{}).(data.Splitter)
	if enc.ShouldSplit("unencodable", 1) {
		t.Error("partial output with an error must not request segmentation")
	}
}
