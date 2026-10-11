// Package multipart provides bounded text reassembly for the receive examples.
package multipart

import (
	"strings"
	"sync"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

const maxMessages = 32
const retention = 5 * time.Minute

type key struct {
	source, destination pdu.Address
	reference           uint16
	referenceType       byte
	coding              byte
}

type message struct {
	parts   []string
	seen    []bool
	count   int
	created time.Time
}

// Receiver holds at most 32 messages of at most 255 parts each. Incomplete
// messages expire after five minutes; this is an example resource policy.
type Receiver struct {
	mu      sync.Mutex
	pending map[key]*message
}

// Add returns completed text, or ready=false while waiting for more parts.
// Invalid concatenation information is ignored and the text is returned alone.
func (r *Receiver) Add(pd *pdu.DeliverSM) (text string, ready bool, err error) {
	text, err = pd.Message.GetMessage()
	if err != nil {
		return "", false, err
	}
	udh := pd.Message.UDH()
	total, sequence, reference, found := udh.GetConcatInfo()
	referenceType := byte(data.UDH_CONCAT_MSG_16_BIT_REF)
	if found {
		// Match GetConcatInfo's last-IE rule; 8-bit and 16-bit references
		// occupy separate spaces (TS 23.040 §§9.2.3.24.1, 9.2.3.24.8).
		for i := len(udh) - 1; i >= 0; i-- {
			if udh[i].ID == data.UDH_CONCAT_MSG_8_BIT_REF || udh[i].ID == data.UDH_CONCAT_MSG_16_BIT_REF {
				referenceType = udh[i].ID
				break
			}
		}
	} else {
		total, sequence, reference, found = pd.GetSARConcatInfo()
	}
	// TS 23.040 V20.0.0 §9.2.3.24.1 (8-bit) and §9.2.3.24.8 (16-bit):
	// ignore the whole concatenation IE for zero total/sequence or sequence > total.
	// Apply the same bounds to SAR TLVs (SMPP v3.4 §§5.3.2.22–24).
	if !found || total == 0 || sequence == 0 || sequence > total {
		return text, true, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil {
		r.pending = make(map[key]*message)
	}
	now := time.Now()
	for k, m := range r.pending {
		if now.Sub(m.created) >= retention {
			delete(r.pending, k)
		}
	}
	var coding byte
	if encoding := pd.Message.Encoding(); encoding != nil {
		coding = encoding.DataCoding()
	}
	k := key{pd.SourceAddr, pd.DestAddr, reference, referenceType, coding}
	m := r.pending[k]
	if m != nil && len(m.parts) != int(total) {
		// A reused reference with a different total must not index the old buffer.
		delete(r.pending, k)
		m = nil
	}
	if m == nil {
		if len(r.pending) >= maxMessages {
			// Keep memory bounded; display this part without retaining another message.
			return text, true, nil
		}
		m = &message{parts: make([]string, total), seen: make([]bool, total), created: now}
		r.pending[k] = m
	}
	i := int(sequence) - 1
	if !m.seen[i] {
		m.parts[i], m.seen[i] = text, true
		m.count++
	}
	if m.count != len(m.parts) {
		return "", false, nil
	}
	delete(r.pending, k)
	return strings.Join(m.parts, ""), true, nil
}
