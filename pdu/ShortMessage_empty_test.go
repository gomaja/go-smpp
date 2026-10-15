package pdu

import (
	"encoding/json"
	"testing"

	"github.com/gomaja/go-smpp/data"
)

func TestEmptyTextMessageDataIsNonNil(t *testing.T) {
	for name, enc := range map[string]data.Encoding{
		"GSM7": data.GSM7BIT, "UTF16": data.UCS2, "strict": data.UCS2Strict,
	} {
		for setter, long := range map[string]bool{"short": false, "long": true} {
			t.Run(name+"/"+setter, func(t *testing.T) {
				var message ShortMessage
				var err error
				if long {
					err = message.SetLongMessageWithEnc("", enc)
				} else {
					err = message.SetMessageWithEncoding("", enc)
				}
				if err != nil {
					t.Fatal(err)
				}
				payload, err := message.GetMessageData()
				if err != nil || payload == nil || len(payload) != 0 {
					t.Fatalf("empty message data: nil=%v, length=%d, err=%v", payload == nil, len(payload), err)
				}
				encoded, err := json.Marshal(payload)
				if err != nil || string(encoded) != `""` {
					t.Errorf("empty message JSON = %s, %v; want empty string", encoded, err)
				}
			})
		}
	}
}
