package meowcaller

import (
	"context"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// recordingSignalIO captures the stanzas the engine writes, standing in for the
// whatsmeow socket.
type recordingSignalIO struct {
	mu   sync.Mutex
	sent []waBinary.Node
	ids  int
}

func (r *recordingSignalIO) GenerateRequestID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids++
	return "req-" + string(rune('0'+r.ids))
}

func (r *recordingSignalIO) SendNode(_ context.Context, node waBinary.Node) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, node)
	return nil
}

func (r *recordingSignalIO) nodes() []waBinary.Node {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]waBinary.Node(nil), r.sent...)
}

// relayLatencyEngine builds an engine holding one call in the given direction,
// with a relay allocation offering ccu1c02 twice (two address families) and
// fdac178c01 once.
func relayLatencyEngine(direction CallDirection) (*engine, *recordingSignalIO) {
	io := &recordingSignalIO{}
	e := &engine{
		c:        &Client{log: zerolog.Nop()},
		signalIO: io,
		calls: map[string]*engineCall{
			"CID": {
				direction:   direction,
				from:        peerJID(),
				creator:     creatorJID(),
				peerDevices: []types.JID{peerJID()},
				relay: &relayData{
					endpoints: []relayEndpoint{
						{relayName: "ccu1c02", c2rRTT: 16, addressBytes: []byte{31, 13, 64, 133, 13, 150}},
						{relayName: "ccu1c02", c2rRTT: 16, addressBytes: []byte{31, 13, 64, 134, 13, 150}},
						{relayName: "fdac178c01", c2rRTT: 7, addressBytes: []byte{202, 136, 90, 96, 13, 152}},
					},
				},
			},
		},
	}
	return e, io
}

func childNode(t *testing.T, parent waBinary.Node, tag string) waBinary.Node {
	t.Helper()
	for _, kid := range parent.GetChildren() {
		if kid.Tag == tag {
			return kid
		}
	}
	t.Fatalf("node %q has no <%s> child", parent.Tag, tag)
	return waBinary.Node{}
}

// TestReportRelayLatencyReportsEachRelayOnce pins the caller's half of the relay
// election: one <relaylatency> per candidate relay name, carrying the server's
// c2r_rtt in the 0x2000000+rtt wire encoding and the endpoint address verbatim,
// addressed to the callee's devices.
func TestReportRelayLatencyReportsEachRelayOnce(t *testing.T) {
	e, io := relayLatencyEngine(CallDirectionOutgoing)
	e.reportRelayLatency("CID")

	sent := io.nodes()
	if len(sent) != 2 {
		t.Fatalf("sent %d stanzas, want 2 (one per distinct relay name)", len(sent))
	}

	byRelay := map[string]waBinary.Node{}
	for _, node := range sent {
		if node.Tag != "call" {
			t.Fatalf("stanza tag = %q, want call", node.Tag)
		}
		if node.Attrs["to"] != peerJID() {
			t.Errorf("stanza to = %v, want %v", node.Attrs["to"], peerJID())
		}
		if id, _ := node.Attrs["id"].(string); id == "" {
			t.Error("stanza has no id; the server cannot route or ack it")
		}
		latency := childNode(t, node, "relaylatency")
		if got := latency.Attrs["call-id"]; got != "CID" {
			t.Errorf("call-id = %v, want CID", got)
		}
		if got := latency.Attrs["call-creator"]; got != creatorJID() {
			t.Errorf("call-creator = %v, want %v", got, creatorJID())
		}
		dest := childNode(t, latency, "destination")
		to := childNode(t, dest, "to")
		if got := to.Attrs["jid"]; got != peerJID() {
			t.Errorf("destination jid = %v, want %v", got, peerJID())
		}
		te := childNode(t, latency, "te")
		byRelay[te.Attrs["relay_name"].(string)] = te
	}

	for _, want := range []struct {
		relay   string
		latency string
		addr    []byte
	}{
		{"ccu1c02", "33554448", []byte{31, 13, 64, 133, 13, 150}},     // 0x2000000 + 16
		{"fdac178c01", "33554439", []byte{202, 136, 90, 96, 13, 152}}, // 0x2000000 + 7
	} {
		te, ok := byRelay[want.relay]
		if !ok {
			t.Errorf("no report for relay %s", want.relay)
			continue
		}
		if got := te.Attrs["latency"]; got != want.latency {
			t.Errorf("%s latency = %v, want %s", want.relay, got, want.latency)
		}
		addr, _ := te.Content.([]byte)
		if string(addr) != string(want.addr) {
			t.Errorf("%s address = %v, want %v", want.relay, addr, want.addr)
		}
	}
}

// TestReportRelayLatencyIsOncePerCall keeps a re-delivered relay allocation from
// restarting the election the callee has already converged on.
func TestReportRelayLatencyIsOncePerCall(t *testing.T) {
	e, io := relayLatencyEngine(CallDirectionOutgoing)
	e.reportRelayLatency("CID")
	e.reportRelayLatency("CID")

	if got := len(io.nodes()); got != 2 {
		t.Fatalf("sent %d stanzas across two reports, want 2", got)
	}
}

// TestReportRelayLatencySkipsIncoming holds the inbound path silent: answering a
// caller's probes with our own numbers is what corrupted its election before.
func TestReportRelayLatencySkipsIncoming(t *testing.T) {
	e, io := relayLatencyEngine(CallDirectionIncoming)
	e.reportRelayLatency("CID")

	if got := len(io.nodes()); got != 0 {
		t.Fatalf("sent %d stanzas on an inbound call, want 0", got)
	}
}

// TestReportRelayLatencyWithoutEndpointsStaysArmed lets an early, endpoint-less
// allocation pass without spending the call's single report.
func TestReportRelayLatencyWithoutEndpointsStaysArmed(t *testing.T) {
	e, io := relayLatencyEngine(CallDirectionOutgoing)
	full := e.calls["CID"].relay
	e.calls["CID"].relay = &relayData{}

	e.reportRelayLatency("CID")
	if got := len(io.nodes()); got != 0 {
		t.Fatalf("sent %d stanzas with no endpoints, want 0", got)
	}

	e.calls["CID"].relay = full
	e.reportRelayLatency("CID")
	if got := len(io.nodes()); got != 2 {
		t.Fatalf("sent %d stanzas once endpoints arrived, want 2", got)
	}
}
