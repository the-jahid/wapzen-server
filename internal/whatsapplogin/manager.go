package whatsapplogin

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"whatsapp-ai-caller-server/internal/agents"
	"whatsapp-ai-caller-server/internal/models"
	"whatsapp-ai-caller-server/internal/phonenumbers"
	"whatsapp-ai-caller-server/internal/voicecall"
)

const firstQRCodeWait = 12 * time.Second

// ErrAlreadyConnected is returned when a login restart is requested for a
// phone number that is still connected.
var ErrAlreadyConnected = errors.New("phone number is already connected")

// ErrLoginInProgress is returned when a login restart is requested while a QR
// login session is still active for the phone number.
var ErrLoginInProgress = errors.New("phone number login is already in progress")

// ErrAgentNotFound is returned when a login names an agent to hand its number to
// that the requesting user does not own.
var ErrAgentNotFound = errors.New("agent not found")

// Errors reported when an outbound call cannot be placed. They are wrapped with
// the specifics, so callers match them with errors.Is and surface the message.
var (
	// ErrCallingUnavailable means the server itself has no voice-call stack
	// configured, so no number can place or answer calls.
	ErrCallingUnavailable = errors.New("voice calling is not configured on this server")

	// ErrNumberNotConnected means the phone number has no live WhatsApp session
	// in this process, so there is nothing to place the call from.
	ErrNumberNotConnected = errors.New("phone number is not connected")

	// ErrCallHandlerUnavailable means the number is connected but was linked with
	// no agent assigned, so its session never installed a call handler. Because
	// meowcaller can only intercept calls from before Connect, the number has to
	// reconnect to gain one (see RefreshInboundCallHandling).
	ErrCallHandlerUnavailable = errors.New("phone number has no call handler attached; reconnect it after assigning an agent")

	// ErrNoLiveAgent means no active agent handles calls in the requested
	// direction for that number.
	ErrNoLiveAgent = errors.New("no live agent for this phone number")

	// ErrAgentUnavailable means an agent was found but the providers it selected
	// have no credentials configured on this server.
	ErrAgentUnavailable = errors.New("agent voice configuration is unavailable")

	// ErrAgentMismatch means the caller named an agent that is not the one
	// assigned to the phone number the call would be placed from.
	ErrAgentMismatch = errors.New("agent is not assigned to this phone number")
)

// Manager owns WhatsApp QR-login sessions and persisted WhatsApp device state.
type Manager struct {
	repo       *phonenumbers.Repository
	agentsRepo *agents.Repository
	container  *sqlstore.Container
	ai         *aiResponder
	voice      *voicecall.Client

	mu       sync.RWMutex
	sessions map[string]*loginSession
}

type loginSession struct {
	phoneNumberID string
	userID        string
	client        *whatsmeow.Client
	cancel        context.CancelFunc
	handlerID     uint32

	// assignAgentID is the agent whose editor started this login, when the login
	// came from one. The number is assigned to that agent as soon as WhatsApp
	// connects, and the inbound-call handler is installed on its behalf up front
	// (see attachInbound), so a scanned number is call-ready without a second
	// reconnect. Empty for logins started from the phone-numbers page and for
	// every resumed session.
	assignAgentID string
	assignOnce    sync.Once

	firstQR     chan struct{}
	firstQROnce sync.Once
	done        chan struct{}
	doneOnce    sync.Once

	mu        sync.Mutex
	connected bool
	closing   bool
	// callClient is the meowcaller client attached to this session, retained so
	// it outlives attachInbound and is not garbage-collected. It is written after
	// the session is already published in the sessions map and read by
	// PlaceOutboundCall from request goroutines, so it lives under mu.
	callClient *meowcaller.Client
}

// NewManager initializes the persistent whatsmeow SQL store using the same
// Postgres database as the application.
// knowledgeRetriever is the vector-store reader wired onto the voice client so
// calls can answer from the agent's knowledge bases. It is passed in rather than
// built here because it must share the embedding model the indexer wrote with.
func NewManager(ctx context.Context, databaseURL string, repo *phonenumbers.Repository, agentsRepo *agents.Repository, callsStore voicecall.CallStore, knowledgeRetriever voicecall.KnowledgeRetriever) (*Manager, error) {
	if repo == nil {
		return nil, fmt.Errorf("phone number repository is required")
	}
	if agentsRepo == nil {
		return nil, fmt.Errorf("agents repository is required")
	}
	if strings.TrimSpace(databaseURL) == "" {
		return nil, fmt.Errorf("database URL is required")
	}
	configureCompanionDeviceProps()

	sqlDB, err := sql.Open("pgx", normalizeDatabaseURL(databaseURL))
	if err != nil {
		return nil, fmt.Errorf("open whatsmeow sql store: %w", err)
	}

	container := sqlstore.NewWithDB(sqlDB, "postgres", nil)
	if err := container.Upgrade(ctx); err != nil {
		_ = container.Close()
		return nil, fmt.Errorf("initialize whatsmeow sql store: %w", err)
	}

	ai := newAIResponder()
	if ai != nil {
		log.Println("whatsapp ai: incoming-message auto-reply enabled (OpenAI)")
	} else {
		log.Println("whatsapp ai: incoming-message auto-reply disabled (set OPENAI_API_KEY to enable)")
	}

	voice := voicecall.New()
	if voice != nil {
		voice.UseCallStore(callsStore)
		voice.UseKnowledgeRetriever(knowledgeRetriever)
		log.Println("voicecall: incoming-call auto-answer enabled (provider selected per agent)")
		if voice.CanRetrieveKnowledge() {
			log.Println("voicecall: in-call knowledge base retrieval enabled for agents with knowledge bases attached")
		} else {
			log.Println("voicecall: in-call knowledge base retrieval disabled (set OPENAI_API_KEY, PINECONE_API_KEY and PINECONE_INDEX_HOST to enable it)")
		}
	} else {
		log.Println("voicecall: incoming-call auto-answer disabled (configure OpenAI or ElevenLabs voice credentials to enable)")
	}

	return &Manager{
		repo:       repo,
		agentsRepo: agentsRepo,
		container:  container,
		ai:         ai,
		voice:      voice,
		sessions:   make(map[string]*loginSession),
	}, nil
}

// configureCompanionDeviceProps makes fresh QR registrations identify as the
// same supported web client used by meowcaller's working reference client.
func configureCompanionDeviceProps() {
	store.DeviceProps.Os = proto.String("Mac OS")
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_CHROME.Enum()
}

// Close disconnects every live WhatsApp session, then closes the voice-call
// diagnostics and underlying whatsmeow SQL store.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	sessions := make([]*loginSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.RUnlock()
	for _, session := range sessions {
		m.finishSession(session, true)
	}

	if err := m.voice.Close(); err != nil {
		log.Printf("voicecall: close client failed: %v", err)
	}
	if m.container == nil {
		return nil
	}
	return m.container.Close()
}

// ResumeSessions reconnects every already-paired WhatsApp number so it resumes
// receiving messages (and AI auto-replies) after a server restart. Without this,
// a "connected" row in the database has no live whatsmeow client behind it, so
// inbound messages are never delivered until the number is re-linked.
func (m *Manager) ResumeSessions(ctx context.Context) {
	if m == nil {
		return
	}

	rows, err := m.repo.ListResumable(ctx)
	if err != nil {
		log.Printf("whatsapp login: list resumable numbers for resume failed: %v", err)
		return
	}
	for _, row := range rows {
		if err := m.resumeSession(ctx, row); err != nil {
			log.Printf("whatsapp login: resume failed phone_number_id=%s: %v", row.ID, err)
		}
	}
}

// resumeSession loads a stored device and reconnects it without a QR scan.
func (m *Manager) resumeSession(ctx context.Context, row models.PhoneNumber) error {
	if m.getSession(row.ID) != nil {
		return nil // already live in this process
	}

	client, err := m.clientFromStoredDevice(ctx, row)
	if err != nil {
		return err
	}
	if client == nil {
		// The row claims connected but the whatsmeow device is gone; make the
		// stored state honest instead of leaving a dead "connected" row.
		log.Printf("whatsapp login: connected row has no stored device on resume; marking disconnected phone_number_id=%s", row.ID)
		_, _ = m.repo.MarkDisconnected(context.Background(), row.UserID, row.ID)
		return nil
	}

	sessionCtx, cancel := context.WithCancel(context.Background())
	client.BackgroundEventCtx = sessionCtx
	session := &loginSession{
		phoneNumberID: row.ID,
		userID:        row.UserID,
		client:        client,
		cancel:        cancel,
		firstQR:       make(chan struct{}),
		done:          make(chan struct{}),
	}
	session.handlerID = client.AddEventHandler(func(evt any) {
		m.handleEvent(session, evt)
	})
	m.storeSession(session)

	// Register the incoming-call handler before Connect so a resumed number is
	// call-ready the moment the server boots, with no QR re-scan. Only numbers with
	// an agent assigned install the handler at all (see attachInbound); the rest run
	// no inbound-call background work and behave like a plain WhatsApp companion.
	m.attachInbound(context.Background(), session, client)

	if err := client.Connect(); err != nil {
		m.finishSession(session, true)
		return fmt.Errorf("connect stored WhatsApp client: %w", err)
	}

	log.Printf("whatsapp login: resumed session phone_number_id=%s wa_jid=%s", row.ID, clientJID(client))
	return nil
}

// StartLogin creates a pending phone number row, starts a new whatsmeow client,
// waits briefly for the first QR code, and returns the current DB state.
//
// assignAgentID, when set, names the agent that gets the number the moment the
// scan connects it: the login was started from that agent's editor, so the
// number it produces is meant for it and the scan alone finishes the setup. It
// must be one of userID's agents — anything else is ErrAgentNotFound. Pass ""
// for a login that belongs to no agent, as the phone-numbers page starts.
func (m *Manager) StartLogin(ctx context.Context, userID string, phoneNumber, label *string, assignAgentID string) (models.PhoneNumber, error) {
	if m == nil {
		return models.PhoneNumber{}, fmt.Errorf("WhatsApp login manager is not configured")
	}

	assignAgentID, err := m.validateAssignAgent(ctx, userID, assignAgentID)
	if err != nil {
		return models.PhoneNumber{}, err
	}

	row, err := m.repo.CreatePendingLogin(ctx, userID, phoneNumber, label)
	if err != nil {
		return models.PhoneNumber{}, err
	}

	return m.startSession(ctx, row, assignAgentID)
}

// validateAssignAgent trims the agent a login wants to assign its number to and
// confirms it belongs to userID, so a bad id fails the login request outright
// rather than silently dropping the assignment once the scan lands. An empty id
// means "assign nothing" and is always valid.
func (m *Manager) validateAssignAgent(ctx context.Context, userID, agentID string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return "", nil
	}
	if m.agentsRepo == nil {
		return "", ErrAgentNotFound
	}
	_, exists, err := m.agentsRepo.LookupPhoneNumberForAgent(ctx, userID, agentID)
	if err != nil {
		return "", fmt.Errorf("look up agent for login assignment: %w", err)
	}
	if !exists {
		return "", ErrAgentNotFound
	}
	return agentID, nil
}

// RestartLogin returns an existing non-connected phone number row to the
// pending_qr state and starts a fresh WhatsApp QR login session for it.
// assignAgentID carries the same meaning as in StartLogin: the agent whose
// editor asked for a new QR code still gets the number when the scan lands.
func (m *Manager) RestartLogin(ctx context.Context, userID, phoneNumberID, assignAgentID string) (models.PhoneNumber, error) {
	if m == nil {
		return models.PhoneNumber{}, fmt.Errorf("WhatsApp login manager is not configured")
	}

	assignAgentID, err := m.validateAssignAgent(ctx, userID, assignAgentID)
	if err != nil {
		return models.PhoneNumber{}, err
	}

	row, err := m.repo.GetByUser(ctx, userID, phoneNumberID)
	if err != nil {
		return models.PhoneNumber{}, err
	}
	if row.Status == models.PhoneNumberStatusConnected {
		// A database row can outlive its whatsmeow device credentials (for
		// example after the device store was cleared independently). Do not
		// trap that stale row in "connected" forever: only block relinking when
		// the corresponding stored device still exists.
		client, loadErr := m.clientFromStoredDevice(ctx, row)
		if loadErr != nil {
			return row, loadErr
		}
		if client != nil {
			return row, ErrAlreadyConnected
		}
		log.Printf("whatsapp login: connected row has no stored device; allowing relink phone_number_id=%s", phoneNumberID)
	}
	if m.getSession(phoneNumberID) != nil {
		return row, ErrLoginInProgress
	}

	row, err = m.repo.ResetPendingLogin(ctx, userID, phoneNumberID)
	if err != nil {
		return models.PhoneNumber{}, err
	}

	return m.startSession(ctx, row, assignAgentID)
}

// RePair unlinks the current companion and starts one fresh QR login while
// preserving the phone-number row and its agent foreign-key assignments.
func (m *Manager) RePair(ctx context.Context, userID, phoneNumberID string) (models.PhoneNumber, error) {
	if m == nil {
		return models.PhoneNumber{}, fmt.Errorf("WhatsApp login manager is not configured")
	}

	row, err := m.repo.GetByUser(ctx, userID, phoneNumberID)
	if err != nil {
		return models.PhoneNumber{}, err
	}
	if row.Status == models.PhoneNumberStatusPendingQR {
		return row, ErrLoginInProgress
	}

	if session := m.getSession(phoneNumberID); session != nil {
		if err := logoutClient(ctx, session.client); err != nil {
			return row, fmt.Errorf("unlink active WhatsApp companion: %w", err)
		}
		m.finishSession(session, false)
	} else {
		client, err := m.clientFromStoredDevice(ctx, row)
		if err != nil {
			return row, err
		}
		if client != nil {
			if err := connectAndLogout(ctx, client); err != nil {
				return row, fmt.Errorf("unlink stored WhatsApp companion: %w", err)
			}
		}
	}

	row, err = m.repo.ResetForRePair(ctx, userID, phoneNumberID)
	if err != nil {
		return models.PhoneNumber{}, err
	}
	log.Printf("whatsapp login: old companion unlinked; starting fresh browser pairing phone_number_id=%s", phoneNumberID)
	// Re-pairing preserves the row and its existing agent assignments, so there
	// is no pending agent to hand the number to when it reconnects.
	return m.startSession(ctx, row, "")
}

// startSession spins up a whatsmeow client for the given pending row, waits
// briefly for the first QR code, and returns the current DB state.
// assignAgentID is the agent this login's number is destined for, or "" when it
// is destined for none (see StartLogin).
func (m *Manager) startSession(ctx context.Context, row models.PhoneNumber, assignAgentID string) (models.PhoneNumber, error) {
	userID := row.UserID

	sessionCtx, cancel := context.WithCancel(context.Background())

	// Reuse an already-paired device when this number still has valid stored
	// WhatsApp credentials, so the companion device ID stays stable instead of
	// climbing on every (re)login. A freshly minted companion device is not yet
	// in a caller's cached device list, so its offers never ring; reusing one
	// stable device is what keeps inbound calls reaching the linked device. Only
	// a genuinely first-time login (no stored device) mints a new device.
	device, err := m.deviceForLogin(ctx, row)
	if err != nil {
		cancel()
		return row, err
	}
	client := whatsmeow.NewClient(device, newWhatsAppLogger(userID, row.ID))
	client.BackgroundEventCtx = sessionCtx

	session := &loginSession{
		phoneNumberID: row.ID,
		userID:        userID,
		client:        client,
		cancel:        cancel,
		assignAgentID: assignAgentID,
		firstQR:       make(chan struct{}),
		done:          make(chan struct{}),
	}

	session.handlerID = client.AddEventHandler(func(evt any) {
		m.handleEvent(session, evt)
	})

	// A paired device (Store.ID != nil) reconnects directly; whatsmeow only
	// issues QR codes for an unpaired device, so a genuinely new login still gets
	// the QR handshake exactly as before while a reused device skips it.
	paired := client.Store.ID != nil
	if !paired {
		qrChan, err := client.GetQRChannel(sessionCtx)
		if err != nil {
			cancel()
			go client.RemoveEventHandler(session.handlerID)
			_, _ = m.repo.MarkFailed(context.Background(), userID, row.ID)
			return row, fmt.Errorf("create QR channel: %w", err)
		}
		m.storeSession(session)
		go m.consumeQRChannel(session, qrChan)
	} else {
		m.storeSession(session)
		log.Printf("whatsapp login: reusing paired device, reconnecting without QR phone_number_id=%s wa_jid=%s", row.ID, clientJID(client))
	}

	// Register the incoming-call handler before Connect (in both the QR and the
	// reuse path) so the number is call-ready the moment it connects. Only numbers
	// with an agent assigned install the handler (see attachInbound); a brand-new QR
	// login started from the phone-numbers page has no agent yet, so it attaches
	// nothing now and is reconnected to pick up the handler once an agent is
	// assigned (RefreshInboundCallHandling). A login started from an agent's editor
	// carries that agent, so it does attach one here.
	m.attachInbound(context.Background(), session, client)

	// BackgroundEventCtx is the QR session context, so Connect is the same
	// direct connection entry point used by the working reference while still
	// allowing terminal session cleanup to cancel background work.
	if err := client.Connect(); err != nil {
		m.finishSession(session, true)
		updated, markErr := m.repo.MarkFailed(context.Background(), userID, row.ID)
		if markErr == nil {
			row = updated
		}
		return row, fmt.Errorf("connect WhatsApp client: %w", err)
	}

	// A reused paired device transitions straight to connected via the event
	// handler, so only wait on the first QR when one is actually coming.
	if !paired {
		select {
		case <-session.firstQR:
		case <-session.done:
		case <-time.After(firstQRCodeWait):
			log.Printf("whatsapp login: first QR was not ready within %s phone_number_id=%s", firstQRCodeWait, row.ID)
		case <-ctx.Done():
		}
	}

	updated, err := m.repo.GetByUser(ctx, userID, row.ID)
	if err != nil {
		return row, err
	}
	return updated, nil
}

// deviceForLogin returns the whatsmeow device to (re)connect for a login. When
// the row already has valid stored WhatsApp credentials — a paired wa_jid whose
// device still lives in the sqlstore — that existing device is reused so the
// companion device ID stays stable across restarts and relinks. Otherwise a
// brand-new device is minted for a first-time pairing. Reusing a device that was
// unlinked remotely surfaces as a LoggedOut event on connect, the same as the
// existing resume path, so no new failure mode is introduced.
func (m *Manager) deviceForLogin(ctx context.Context, row models.PhoneNumber) (*store.Device, error) {
	if row.WAJID != nil && strings.TrimSpace(*row.WAJID) != "" {
		jid, err := types.ParseJID(strings.TrimSpace(*row.WAJID))
		if err != nil {
			log.Printf("whatsapp login: stored WhatsApp JID %q is invalid, minting new device phone_number_id=%s: %v", *row.WAJID, row.ID, err)
		} else {
			device, err := m.container.GetDevice(ctx, jid)
			if err != nil {
				return nil, fmt.Errorf("load stored WhatsApp device: %w", err)
			}
			if device != nil {
				return device, nil
			}
		}
	}
	return m.container.NewDevice(), nil
}

// resolveInboundCallAgent decides, at call time, whether an inbound WhatsApp call
// to phoneNumberID is answered by the AI and with which prompt. Every paired
// number answers its own calls; the only gate is the agent assigned to it. It
// returns nil to decline the call — leaving normal WhatsApp ringing behavior so
// the caller reaches the human on the phone as usual — when no live,
// inbound-capable agent is assigned to the number (unassigned, paused, or
// outbound-only), when its provider is unconfigured, or when the lookup itself
// fails (fail closed: never answer with the wrong prompt).
func (m *Manager) resolveInboundCallAgent(ctx context.Context, userID, phoneNumberID string) *voicecall.CallAgent {
	call, err := m.resolveCallAgent(ctx, userID, phoneNumberID, models.CallTypeInbound)
	if err != nil {
		log.Printf("voicecall: not answering inbound calls on phone_number_id=%s: %v; leaving call to normal WhatsApp behavior", phoneNumberID, err)
		return nil
	}
	log.Printf("voicecall: live inbound agent %s answering call on phone_number_id=%s", call.AgentID, phoneNumberID)
	return call
}

// resolveCallAgent loads the live agent that handles calls on phoneNumberID in
// the given direction (models.CallTypeInbound or CallTypeOutbound) and renders
// it into the per-call configuration voicecall runs the conversation from.
//
// It reports why no agent is available rather than just declining, because the
// outbound path answers an API request that has to explain itself; the inbound
// path collapses every error back to "leave the call alone".
func (m *Manager) resolveCallAgent(ctx context.Context, userID, phoneNumberID, direction string) (*voicecall.CallAgent, error) {
	if m.voice == nil || m.agentsRepo == nil {
		return nil, ErrCallingUnavailable
	}

	lookup := m.agentsRepo.LiveInboundAgentForPhoneNumber
	if direction == models.CallTypeOutbound {
		lookup = m.agentsRepo.LiveOutboundAgentForPhoneNumber
	}
	agent, err := lookup(ctx, phoneNumberID)
	if err != nil {
		return nil, fmt.Errorf("resolve live %s agent: %w", direction, err)
	}
	if agent == nil {
		return nil, fmt.Errorf("%w: no active %s-capable agent is assigned to this phone number", ErrNoLiveAgent, direction)
	}
	if !m.voice.CanHandleProvider(agent.VoiceProvider) {
		return nil, fmt.Errorf("%w: agent %s selected voice provider %q, which is not configured on this server", ErrAgentUnavailable, agent.ID, agent.VoiceProvider)
	}
	// Native Realtime owns response generation inside the live session. The
	// request-based OpenAI and ElevenLabs pipelines still need a text-model key.
	if agent.VoiceProvider != "openai_realtime" && !m.voice.CanHandleLLM(agent.ModelProvider) {
		return nil, fmt.Errorf("%w: agent %s selected model provider %q, which is not configured on this server", ErrAgentUnavailable, agent.ID, agent.ModelProvider)
	}
	call := buildCallAgent(userID, phoneNumberID, agent)
	call.KnowledgeBases = m.knowledgeBasesForCall(ctx, agent.ID)
	call.Tools = m.toolsForCall(ctx, agent.ID)
	call.SendMessage = m.messageSenderFor(phoneNumberID, userID)
	return call, nil
}

// toolsForCall loads the actions the agent may take during the call. As with
// its knowledge bases, a failure here is not a reason to drop the call: an agent
// that cannot load its tools still holds the conversation, which is a far better
// outcome for the caller than an unanswered phone.
func (m *Manager) toolsForCall(ctx context.Context, agentID string) []models.AgentTool {
	if m.agentsRepo == nil {
		return nil
	}
	tools, err := m.agentsRepo.ToolsForAgent(ctx, agentID)
	if err != nil {
		log.Printf("voicecall: could not load tools for agent %s: %v; the call runs without them", agentID, err)
		return nil
	}
	return tools
}

// messageSenderFor returns the function the send_text tool uses to message the
// other party mid-call, bound to the number the call runs on.
//
// The session is resolved when the message is sent rather than now, because a
// call outlives any single lookup: a number that reconnects during a call gets a
// new whatsmeow client, and a sender captured at answer time would be writing to
// the old one. Nil is never returned — the closure reports the failure instead,
// which is what the tool tells the model.
func (m *Manager) messageSenderFor(phoneNumberID, userID string) func(context.Context, types.JID, string) error {
	return func(ctx context.Context, peer types.JID, body string) error {
		session := m.getSession(phoneNumberID)
		if session == nil || session.userID != userID || !session.isConnected() {
			return fmt.Errorf("phone number %s is not connected", phoneNumberID)
		}
		client := session.client
		if client == nil {
			return fmt.Errorf("phone number %s has no WhatsApp client", phoneNumberID)
		}
		if _, err := client.SendMessage(ctx, peer, &waE2E.Message{Conversation: proto.String(body)}); err != nil {
			return fmt.Errorf("send WhatsApp message: %w", err)
		}
		return nil
	}
}

// knowledgeBasesForCall loads the knowledge bases the agent may answer from.
// A failure here is not a reason to drop the call: an agent that cannot reach
// its knowledge bases still answers from its prompt, which is a far better
// outcome for the caller than an unanswered phone. The lookup is skipped
// entirely when retrieval is not configured, since nothing could query them.
func (m *Manager) knowledgeBasesForCall(ctx context.Context, agentID string) []models.AgentKnowledgeBase {
	if !m.voice.CanRetrieveKnowledge() {
		return nil
	}
	bases, err := m.agentsRepo.KnowledgeBasesForAgent(ctx, agentID)
	if err != nil {
		log.Printf("voicecall: could not load knowledge bases for agent %s: %v; answering from its prompt alone", agentID, err)
		return nil
	}
	return bases
}

// buildCallAgent renders a stored agent into one call's configuration: the
// instructions come from its General Prompt, alongside
// its welcome-message, transcriber, and voice settings.
func buildCallAgent(userID, phoneNumberID string, agent *agents.InboundAgent) *voicecall.CallAgent {
	call := &voicecall.CallAgent{
		UserID:           userID,
		PhoneNumberID:    phoneNumberID,
		AgentID:          agent.ID,
		Provider:         agent.VoiceProvider,
		LLMProvider:      agent.ModelProvider,
		LLMModel:         agent.ModelName,
		LLMTemperature:   agent.ModelTemperature,
		Instructions:     composeAgentInstructions(agent),
		BeginMessageMode: agent.BeginMessageMode,
		WelcomeMessage:   agent.WelcomeMessage,
		WelcomeDelayMs:   agent.WelcomeDelayMs,
		Language:         agent.Language,
	}
	if agent.TranscriberProvider == "openai" {
		call.TranscribeModel = agent.TranscriberOpenAIModel
	}
	if agent.VoiceProvider == "openai" || agent.VoiceProvider == "openai_realtime" {
		call.Voice = agent.VoiceOpenAIVoiceID
		call.OpenAIVoiceModel = agent.VoiceOpenAIVoiceModel
		call.RealtimeModel = agent.VoiceOpenAIRealtimeModel
		call.VoiceInstructions = agent.VoiceOpenAIInstructions
		call.Speed = agent.VoiceOpenAISpeed
		call.Volume = agent.VoiceOpenAIVolume
	} else if agent.VoiceProvider == "11labs" {
		call.ElevenLabsVoiceID = agent.VoiceElevenLabsVoiceID
		call.ElevenLabsModel = agent.VoiceElevenLabsModel
		call.ElevenLabsTranscribeModel = agent.TranscriberElevenLabsModel
		// The existing speed/volume columns back the provider-neutral controls in
		// the editor. ElevenLabs receives speed as an override; volume is local.
		call.Speed = agent.VoiceOpenAISpeed
		call.Volume = agent.VoiceOpenAIVolume
	}
	return call
}

// languageNames maps the language codes offered by the dashboard's Language
// dropdown (frontend/src/components/agents/AgentsPage.tsx) to their display
// names, so composeAgentInstructions can spell out the agent's language for
// the model instead of relying on it to infer a locale code correctly.
var languageNames = map[string]string{
	"af-ZA": "Afrikaans", "sq-AL": "Albanian", "am-ET": "Amharic", "ar-SA": "Arabic",
	"hy-AM": "Armenian", "az-AZ": "Azerbaijani", "eu-ES": "Basque", "be-BY": "Belarusian",
	"bn-BD": "Bengali (Bangladesh)", "bs-BA": "Bosnian", "bg-BG": "Bulgarian", "my-MM": "Burmese",
	"ca-ES": "Catalan", "zh-CN": "Chinese (Simplified)", "zh-TW": "Chinese (Traditional)",
	"hr-HR": "Croatian", "cs-CZ": "Czech", "da-DK": "Danish", "nl-NL": "Dutch",
	"en-AU": "English (Australia)", "en-CA": "English (Canada)", "en-IN": "English (India)",
	"en-GB": "English (UK)", "en-US": "English (US)", "et-EE": "Estonian", "fil-PH": "Filipino",
	"fi-FI": "Finnish", "fr-CA": "French (Canada)", "fr-FR": "French (France)", "gl-ES": "Galician",
	"ka-GE": "Georgian", "de-AT": "German (Austria)", "de-DE": "German (Germany)",
	"de-CH": "German (Switzerland)", "el-GR": "Greek", "gu-IN": "Gujarati", "he-IL": "Hebrew",
	"hi-IN": "Hindi", "hu-HU": "Hungarian", "is-IS": "Icelandic", "id-ID": "Indonesian",
	"ga-IE": "Irish", "it-IT": "Italian", "ja-JP": "Japanese", "jv-ID": "Javanese",
	"kn-IN": "Kannada", "kk-KZ": "Kazakh", "km-KH": "Khmer", "ko-KR": "Korean", "lo-LA": "Lao",
	"lv-LV": "Latvian", "lt-LT": "Lithuanian", "mk-MK": "Macedonian", "ms-MY": "Malay",
	"ml-IN": "Malayalam", "mr-IN": "Marathi", "mn-MN": "Mongolian", "ne-NP": "Nepali",
	"no-NO": "Norwegian", "fa-IR": "Persian", "pl-PL": "Polish", "pt-BR": "Portuguese (Brazil)",
	"pt-PT": "Portuguese (Portugal)", "pa-IN": "Punjabi", "ro-RO": "Romanian", "ru-RU": "Russian",
	"sr-RS": "Serbian", "si-LK": "Sinhala", "sk-SK": "Slovak", "sl-SI": "Slovenian",
	"es-MX": "Spanish (Mexico)", "es-ES": "Spanish (Spain)", "es-US": "Spanish (US)",
	"sw-KE": "Swahili", "sv-SE": "Swedish", "ta-IN": "Tamil", "te-IN": "Telugu", "th-TH": "Thai",
	"tr-TR": "Turkish", "uk-UA": "Ukrainian", "ur-PK": "Urdu", "uz-UZ": "Uzbek",
	"vi-VN": "Vietnamese", "cy-GB": "Welsh", "zu-ZA": "Zulu",
}

// composeAgentInstructions renders the agent's prompt fields into a single
// instruction string for the selected conversation model: a hard language-lock
// directive (from the dashboard's Language setting), then the General Prompt
// (system prompt). Returns "" when the agent
// has no prompt content and no language set, which lets voicecall fall back
// to its default instructions.
func composeAgentInstructions(agent *agents.InboundAgent) string {
	var b strings.Builder
	if lang := strings.TrimSpace(agent.Language); lang != "" {
		name, ok := languageNames[lang]
		if !ok {
			name = lang
		}
		b.WriteString(fmt.Sprintf(
			"Language: You must speak and understand only %s (%s) for this entire call. "+
				"Always respond in %s, even if the caller speaks to you in a different language — "+
				"in that case, politely continue the conversation in %s rather than switching.",
			name, lang, name, name,
		))
	}
	if sp := strings.TrimSpace(agent.SystemPrompt); sp != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(sp)
	}

	if style := strings.TrimSpace(agent.VoiceOpenAIInstructions); style != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Vocal delivery style: ")
		b.WriteString(style)
	}

	return strings.TrimSpace(b.String())
}

// attachInbound installs the incoming-call handler on the session's whatsmeow
// client, but only when an agent is assigned to the number. It MUST run before
// client.Connect (meowcaller installs its low-level <ack>/<call> interception at
// construction, which must be in place before the receive loop starts). A number
// with no agent installs nothing, so it runs no inbound-call background work and
// behaves like a plain WhatsApp companion; when an agent is later assigned,
// RefreshInboundCallHandling reconnects the number to install the handler.
func (m *Manager) attachInbound(ctx context.Context, session *loginSession, client *whatsmeow.Client) {
	if m.voice == nil {
		return
	}
	// A login started from an agent's editor is assigned to that agent the
	// instant it connects, which is already too late to install the handler.
	// Counting that pending assignment here is what makes a scanned number
	// call-ready straight away instead of only after a reconnect.
	if session.assignAgentID == "" && !m.shouldAttachInbound(ctx, session.phoneNumberID) {
		log.Printf("voicecall: no agent assigned to phone_number_id=%s; not installing inbound call handler", session.phoneNumberID)
		return
	}
	phoneNumberID := session.phoneNumberID
	userID := session.userID
	session.setCallClient(m.voice.Attach(client, func() *voicecall.CallAgent {
		return m.resolveInboundCallAgent(context.Background(), userID, phoneNumberID)
	}))
}

// assignPendingAgent hands the number that just connected to the agent whose
// editor started this login, so scanning the QR code is the whole setup: nothing
// to pick on the phone-numbers page afterwards, and no publish needed to make the
// assignment real.
//
// It runs at most once per session, because a later reconnect must not claw the
// number back from an agent the owner has since moved it to. An agent that
// gained a number while the QR was on screen keeps it — the repository refuses
// to overwrite one — and any failure simply leaves the number unassigned, which
// the dashboard still lists as one ready to pick.
func (m *Manager) assignPendingAgent(session *loginSession) {
	if session.assignAgentID == "" || m.agentsRepo == nil {
		return
	}
	session.assignOnce.Do(func() {
		assigned, err := m.agentsRepo.AssignPhoneNumberIfUnassigned(
			context.Background(),
			session.userID,
			session.assignAgentID,
			session.phoneNumberID,
		)
		switch {
		case err != nil:
			log.Printf(
				"whatsapp login: assigning phone_number_id=%s to agent %s failed: %v",
				session.phoneNumberID, session.assignAgentID, err,
			)
		case !assigned:
			log.Printf(
				"whatsapp login: agent %s already has a phone number; leaving phone_number_id=%s unassigned",
				session.assignAgentID, session.phoneNumberID,
			)
		default:
			log.Printf(
				"whatsapp login: phone_number_id=%s assigned to agent %s that started the login",
				session.phoneNumberID, session.assignAgentID,
			)
		}
	})
}

// OutboundCall describes one call to place. UserID, PhoneNumberID and To are
// required; the rest narrow what the call is and who it is recorded against.
//
// ExpectAgentID asserts which agent speaks: the agent is always the one assigned
// to the number, so a value naming a different one is ErrAgentMismatch rather
// than a call placed by an agent the caller did not intend. Empty skips the
// check.
//
// CampaignID and LeadID mark the call as an outbound campaign's, which is what
// lets the campaign list it and what advances the campaign's counters and the
// lead's status as the call progresses. Both empty places an ordinary one-off
// call belonging to no campaign.
type OutboundCall struct {
	UserID        string
	PhoneNumberID string
	To            string
	ExpectAgentID string
	CampaignID    string
	LeadID        string
}

// PlaceOutboundCall calls req.To from the user's phone number, with the agent
// assigned to that number speaking. It returns the calls-table row id of the new
// call, which the API hands back so the caller can follow it — the call itself is
// still ringing when this returns and progresses in the background.
//
// The number must be connected in this process and must have had an agent
// assigned when it connected (that is what installs its call handler). Failures
// are the sentinel errors above, wrapped with their specifics.
func (m *Manager) PlaceOutboundCall(ctx context.Context, req OutboundCall) (string, error) {
	if m == nil || m.voice == nil {
		return "", ErrCallingUnavailable
	}

	session := m.getSession(req.PhoneNumberID)
	// A session belonging to someone else is reported as "not connected": the
	// caller has already been shown to own this number, so a mismatch here is a
	// bug rather than something to describe back to them.
	if session == nil || session.userID != req.UserID || !session.isConnected() {
		return "", ErrNumberNotConnected
	}
	callClient := session.callCapableClient()
	if callClient == nil {
		return "", ErrCallHandlerUnavailable
	}

	agent, err := m.resolveCallAgent(ctx, req.UserID, req.PhoneNumberID, models.CallTypeOutbound)
	if err != nil {
		return "", err
	}
	if expect := strings.TrimSpace(req.ExpectAgentID); expect != "" && expect != agent.AgentID {
		return "", fmt.Errorf("%w: this number is assigned to agent %s", ErrAgentMismatch, agent.AgentID)
	}
	// Stamped on the agent rather than passed down beside it, because the call
	// record is written from the agent at insert time.
	agent.CampaignID = strings.TrimSpace(req.CampaignID)
	agent.LeadID = strings.TrimSpace(req.LeadID)

	log.Printf("voicecall: agent %s placing outbound call from phone_number_id=%s", agent.AgentID, req.PhoneNumberID)
	return m.voice.PlaceCall(ctx, callClient, req.To, agent)
}

// shouldAttachInbound reports whether the inbound-call handler should be installed
// for phoneNumberID at connect time: true only when at least one agent is assigned
// to the number. Fails closed (skip attach) when voice is disabled or the lookup
// errors. The finer active/inbound-direction decision is made per call by
// resolveInboundCallAgent; this is only the coarse "is anyone using this number
// for calls at all" gate that keeps unused numbers out of the call stack.
func (m *Manager) shouldAttachInbound(ctx context.Context, phoneNumberID string) bool {
	if m.voice == nil || m.agentsRepo == nil {
		return false
	}
	assigned, err := m.agentsRepo.HasAgentForPhoneNumber(ctx, phoneNumberID)
	if err != nil {
		log.Printf("voicecall: could not check agent assignment for phone_number_id=%s: %v", phoneNumberID, err)
		return false
	}
	return assigned
}

// RefreshInboundCallHandling reconnects the live WhatsApp session for
// phoneNumberID so its inbound-call handler is installed or removed to match the
// number's current agent assignment. Call it after an agent's phone-number
// assignment changes (assigned, reassigned, unassigned, or the agent deleted).
// Because meowcaller's call interception can only be installed before Connect, a
// running number picks up an assignment change by reconnecting, not live.
//
// It runs in the background and is a no-op when voice is disabled or the number
// has no connected session — an absent or still-connecting session evaluates the
// attach gate correctly the next time it connects.
func (m *Manager) RefreshInboundCallHandling(phoneNumberID string) {
	if m == nil || m.voice == nil {
		return
	}
	go m.refreshInboundCallHandling(strings.TrimSpace(phoneNumberID))
}

func (m *Manager) refreshInboundCallHandling(phoneNumberID string) {
	if phoneNumberID == "" {
		return
	}
	session := m.getSession(phoneNumberID)
	if session == nil || session.isClosing() || !session.isConnected() {
		return
	}

	ctx := context.Background()
	row, err := m.repo.GetByUser(ctx, session.userID, phoneNumberID)
	if err != nil {
		log.Printf("whatsapp login: refresh inbound call handling: load number failed phone_number_id=%s: %v", phoneNumberID, err)
		return
	}

	log.Printf("voicecall: reconnecting phone_number_id=%s to apply agent assignment change", phoneNumberID)
	// Drop the current client (with its now-stale attach state) and resume a fresh
	// one, which re-evaluates the attach gate against the new assignment.
	m.finishSession(session, true)
	if err := m.resumeSession(ctx, row); err != nil {
		log.Printf("whatsapp login: refresh inbound call handling: reconnect failed phone_number_id=%s: %v", phoneNumberID, err)
	}
}

// Logout disconnects/unlinks the WhatsApp client when possible, deletes the
// local WhatsApp device store, then removes the user-owned phone number row.
// The remote WhatsApp unlink is best-effort: stale credentials or a temporarily
// unreachable WhatsApp connection must not prevent removing the local number.
func (m *Manager) Logout(ctx context.Context, userID, phoneNumberID string) (models.PhoneNumber, error) {
	if m == nil {
		return models.PhoneNumber{}, fmt.Errorf("WhatsApp login manager is not configured")
	}

	row, err := m.repo.GetByUser(ctx, userID, phoneNumberID)
	if err != nil {
		return models.PhoneNumber{}, err
	}

	session := m.getSession(phoneNumberID)
	if session != nil {
		if err := logoutClient(ctx, session.client); err != nil {
			log.Printf("whatsapp login: logout active client failed phone_number_id=%s: %v", phoneNumberID, err)
			if cleanupErr := deleteClientStore(ctx, session.client); cleanupErr != nil {
				log.Printf("whatsapp login: delete active device store failed phone_number_id=%s: %v", phoneNumberID, cleanupErr)
			}
			m.finishSession(session, true)
		} else {
			m.finishSession(session, false)
		}
		return m.repo.DeleteByUser(ctx, userID, phoneNumberID)
	}

	client, err := m.clientFromStoredDevice(ctx, row)
	if err != nil {
		log.Printf("whatsapp login: load stored device for logout failed phone_number_id=%s: %v", phoneNumberID, err)
	} else if client != nil {
		if err := connectAndLogout(ctx, client); err != nil {
			log.Printf("whatsapp login: logout stored client failed phone_number_id=%s: %v", phoneNumberID, err)
			if cleanupErr := deleteClientStore(ctx, client); cleanupErr != nil {
				log.Printf("whatsapp login: delete stored device failed phone_number_id=%s: %v", phoneNumberID, cleanupErr)
			}
		}
	}

	return m.repo.DeleteByUser(ctx, userID, phoneNumberID)
}

func (m *Manager) consumeQRChannel(session *loginSession, qrChan <-chan whatsmeow.QRChannelItem) {
	for item := range qrChan {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			qrCode, err := qrDataURL(item.Code)
			if err != nil {
				log.Printf("whatsapp login: render QR failed for phone_number_id=%s: %v", session.phoneNumberID, err)
				continue
			}
			if _, err := m.repo.SetQRCode(context.Background(), session.userID, session.phoneNumberID, qrCode); err != nil {
				log.Printf("whatsapp login: store QR failed for phone_number_id=%s: %v", session.phoneNumberID, err)
				continue
			}
			session.signalFirstQR()
		case "success":
			log.Printf("whatsapp login: QR pairing succeeded phone_number_id=%s", session.phoneNumberID)
			// Connected will follow after the post-pairing reconnect.
		case "timeout":
			log.Printf("whatsapp login: QR session expired phone_number_id=%s", session.phoneNumberID)
			if _, err := m.repo.MarkExpired(context.Background(), session.userID, session.phoneNumberID); err != nil {
				log.Printf("whatsapp login: mark expired failed for phone_number_id=%s: %v", session.phoneNumberID, err)
			}
			m.finishSession(session, true)
		default:
			if item.Event == whatsmeow.QRChannelEventError && item.Error != nil {
				log.Printf("whatsapp login: QR pairing error for phone_number_id=%s: %v", session.phoneNumberID, item.Error)
			} else {
				log.Printf("whatsapp login: QR pairing ended for phone_number_id=%s event=%s reason=%s", session.phoneNumberID, item.Event, qrChannelFailureReason(item))
			}
			if _, err := m.repo.MarkFailed(context.Background(), session.userID, session.phoneNumberID); err != nil {
				log.Printf("whatsapp login: mark failed failed for phone_number_id=%s: %v", session.phoneNumberID, err)
			}
			m.finishSession(session, true)
		}
	}
}

func (m *Manager) handleEvent(session *loginSession, evt any) {
	switch v := evt.(type) {
	case *events.Message:
		m.handleIncomingMessage(session, v)
	case *events.Connected:
		waJID := clientJID(session.client)
		if waJID == "" || !session.client.IsLoggedIn() {
			return
		}
		session.setConnected()
		// A restored companion connection needs to announce itself as active.
		// Besides presence, whatsmeow uses this to refresh the unified-session
		// marker that the primary phone and WhatsApp services use for active web
		// sessions. Fresh pairings send that marker during the pairing handshake;
		// reconnects otherwise never send it in this server.
		//
		// It is gated because announcing PresenceAvailable marks this companion as an
		// active web session, which may make WhatsApp route inbound call audio to the
		// phone/another device and leave this one with DTX-only silence. meowcaller-test
		// never announces presence and receives the caller's real audio. Set
		// WHATSAPP_ANNOUNCE_PRESENCE=false to match that while testing calls.
		if announcePresenceEnabled() {
			go announceAvailable(session)
		} else {
			log.Printf("whatsapp login: presence announce disabled (WHATSAPP_ANNOUNCE_PRESENCE=false) phone_number_id=%s", session.phoneNumberID)
		}
		if _, err := m.repo.MarkConnected(
			context.Background(),
			session.userID,
			session.phoneNumberID,
			waJID,
			phoneNumberFromJID(session.client.Store.GetJID()),
		); err != nil {
			log.Printf("whatsapp login: mark connected failed for phone_number_id=%s: %v", session.phoneNumberID, err)
			return
		}
		m.assignPendingAgent(session)
	case *events.Disconnected:
		if session.isConnected() && !session.isClosing() {
			if _, err := m.repo.MarkDisconnected(context.Background(), session.userID, session.phoneNumberID); err != nil {
				log.Printf("whatsapp login: mark disconnected failed for phone_number_id=%s: %v", session.phoneNumberID, err)
			}
		}
	case *events.LoggedOut:
		if !session.isClosing() {
			if _, err := m.repo.MarkDisconnected(context.Background(), session.userID, session.phoneNumberID); err != nil {
				log.Printf("whatsapp login: mark logged-out disconnected failed for phone_number_id=%s: %v", session.phoneNumberID, err)
			}
		}
		m.finishSession(session, false)
	case *events.StreamReplaced:
		if !session.isClosing() {
			if _, err := m.repo.MarkDisconnected(context.Background(), session.userID, session.phoneNumberID); err != nil {
				log.Printf("whatsapp login: mark stream-replaced disconnected failed for phone_number_id=%s: %v", session.phoneNumberID, err)
			}
		}
		m.finishSession(session, false)
	case *events.ClientOutdated:
		m.markLifecycleFailure(session, "client outdated")
	case *events.ConnectFailure:
		m.markLifecycleFailure(session, v.Message)
	case *events.TemporaryBan:
		m.markLifecycleFailure(session, v.String())
	}
}

// announcePresenceEnabled reports whether the server should announce
// PresenceAvailable on connect. Defaults on (it keeps inbound messages flowing
// after a reconnect); set WHATSAPP_ANNOUNCE_PRESENCE=false to suppress it, which
// mirrors meowcaller-test and is used to test whether the available-presence
// marker is what diverts inbound call audio away from this companion.
func announcePresenceEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WHATSAPP_ANNOUNCE_PRESENCE"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func announceAvailable(session *loginSession) {
	if session == nil || session.client == nil || session.isClosing() {
		return
	}
	if strings.TrimSpace(session.client.Store.PushName) == "" {
		session.client.Store.PushName = "WhatsApp AI Caller"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := session.client.SendPresence(ctx, types.PresenceAvailable); err != nil {
		log.Printf("whatsapp login: announce active companion failed phone_number_id=%s: %v", session.phoneNumberID, err)
		return
	}
	log.Printf("whatsapp login: active companion announced phone_number_id=%s wa_jid=%s", session.phoneNumberID, clientJID(session.client))
}

// handleIncomingMessage answers an inbound WhatsApp text message with a short
// AI reply. It is a no-op unless the OpenAI responder is configured, and it
// skips our own messages and group chats to avoid reply loops and noise.
func (m *Manager) handleIncomingMessage(session *loginSession, evt *events.Message) {
	if m.ai == nil {
		return
	}
	if evt.Info.IsFromMe || evt.Info.IsGroup {
		return
	}
	text := strings.TrimSpace(extractTextMessage(evt.Message))
	if text == "" {
		return
	}

	chat := evt.Info.Chat
	client := session.client
	phoneNumberID := session.phoneNumberID

	// Reply off the event goroutine so the OpenAI round trip never blocks
	// whatsmeow's event delivery.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		reply, err := m.ai.Reply(ctx, text)
		if err != nil {
			log.Printf("whatsapp ai: reply failed phone_number_id=%s: %v", phoneNumberID, err)
			return
		}
		if reply = strings.TrimSpace(reply); reply == "" {
			return
		}

		if _, err := client.SendMessage(ctx, chat, &waE2E.Message{Conversation: proto.String(reply)}); err != nil {
			log.Printf("whatsapp ai: send reply failed phone_number_id=%s: %v", phoneNumberID, err)
			return
		}
		log.Printf("whatsapp ai: replied to %s phone_number_id=%s", chat.String(), phoneNumberID)
	}()
}

// extractTextMessage pulls plain text out of the two common text message shapes:
// a simple conversation string and an extended text message (links, replies).
func extractTextMessage(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if text := msg.GetConversation(); text != "" {
		return text
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	return ""
}

func qrChannelFailureReason(item whatsmeow.QRChannelItem) string {
	switch item.Event {
	case whatsmeow.QRChannelEventError:
		if item.Error != nil {
			return item.Error.Error()
		}
		return "pairing error"
	case whatsmeow.QRChannelClientOutdated.Event:
		return "client outdated"
	case whatsmeow.QRChannelScannedWithoutMultidevice.Event:
		return "QR was scanned without multidevice enabled"
	case whatsmeow.QRChannelErrUnexpectedEvent.Event:
		return "unexpected connection event before QR pairing completed"
	default:
		if strings.TrimSpace(item.Event) == "" {
			return "QR channel closed without an event"
		}
		return item.Event
	}
}

func (m *Manager) markLifecycleFailure(session *loginSession, reason string) {
	if strings.TrimSpace(reason) != "" {
		log.Printf("whatsapp login: lifecycle failure phone_number_id=%s reason=%s", session.phoneNumberID, reason)
	}
	if _, err := m.repo.MarkFailed(context.Background(), session.userID, session.phoneNumberID); err != nil {
		log.Printf("whatsapp login: mark lifecycle failed failed for phone_number_id=%s: %v", session.phoneNumberID, err)
	}
	m.finishSession(session, true)
}

func (m *Manager) storeSession(session *loginSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.phoneNumberID] = session
}

func (m *Manager) getSession(phoneNumberID string) *loginSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[phoneNumberID]
}

func (m *Manager) finishSession(session *loginSession, disconnect bool) {
	session.doneOnce.Do(func() {
		session.setClosing()
		session.cancel()
		if disconnect {
			session.client.Disconnect()
		}
		go session.client.RemoveEventHandler(session.handlerID)
		close(session.done)

		m.mu.Lock()
		if m.sessions[session.phoneNumberID] == session {
			delete(m.sessions, session.phoneNumberID)
		}
		m.mu.Unlock()
	})
}

func (m *Manager) clientFromStoredDevice(ctx context.Context, row models.PhoneNumber) (*whatsmeow.Client, error) {
	if row.WAJID == nil || strings.TrimSpace(*row.WAJID) == "" {
		return nil, nil
	}

	jid, err := types.ParseJID(strings.TrimSpace(*row.WAJID))
	if err != nil {
		return nil, fmt.Errorf("parse stored WhatsApp JID: %w", err)
	}

	device, err := m.container.GetDevice(ctx, jid)
	if err != nil {
		return nil, fmt.Errorf("load stored WhatsApp device: %w", err)
	}
	if device == nil {
		return nil, nil
	}

	return whatsmeow.NewClient(device, newWhatsAppLogger(row.UserID, row.ID)), nil
}

func newWhatsAppLogger(userID, phoneNumberID string) waLog.Logger {
	logger := zerolog.New(log.Writer()).Level(whatsAppLogLevel()).With().
		Str("component", "whatsmeow").
		Str("user_id", userID).
		Str("phone_number_id", phoneNumberID).
		Logger()
	return waLog.Zerolog(logger)
}

// whatsAppLogLevel resolves WHATSAPP_LOG_LEVEL, defaulting to info. At debug
// whatsmeow logs every stanza it sends and receives, which is the only way to
// read call signalling — the meowcaller diagnostics cover the media plane only.
func whatsAppLogLevel() zerolog.Level {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("WHATSAPP_LOG_LEVEL")))
	if raw == "" {
		return zerolog.InfoLevel
	}
	level, err := zerolog.ParseLevel(raw)
	if err != nil || level == zerolog.NoLevel {
		log.Printf("whatsapp login: ignoring invalid WHATSAPP_LOG_LEVEL=%q, using info", raw)
		return zerolog.InfoLevel
	}
	return level
}

func logoutClient(ctx context.Context, client *whatsmeow.Client) error {
	if client == nil {
		return nil
	}
	if client.IsLoggedIn() {
		if err := client.Logout(ctx); err == nil {
			return nil
		} else if !errors.Is(err, whatsmeow.ErrNotLoggedIn) {
			return fmt.Errorf("logout WhatsApp client: %w", err)
		}
	}
	client.Disconnect()
	if client.Store != nil && client.Store.ID != nil {
		if err := client.Store.Delete(ctx); err != nil {
			return fmt.Errorf("delete WhatsApp device store: %w", err)
		}
	}
	return nil
}

func deleteClientStore(ctx context.Context, client *whatsmeow.Client) error {
	if client == nil {
		return nil
	}
	client.Disconnect()
	if client.Store == nil || client.Store.ID == nil {
		return nil
	}
	if err := client.Store.Delete(ctx); err != nil {
		return fmt.Errorf("delete WhatsApp device store: %w", err)
	}
	return nil
}

func connectAndLogout(ctx context.Context, client *whatsmeow.Client) error {
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := client.ConnectContext(connectCtx); err != nil && !errors.Is(err, whatsmeow.ErrAlreadyConnected) {
		client.Disconnect()
		return fmt.Errorf("connect stored WhatsApp client: %w", err)
	}

	if !client.WaitForConnection(20 * time.Second) {
		client.Disconnect()
		return fmt.Errorf("stored WhatsApp client did not become connected")
	}

	return logoutClient(ctx, client)
}

func (s *loginSession) signalFirstQR() {
	s.firstQROnce.Do(func() { close(s.firstQR) })
}

func (s *loginSession) setConnected() {
	s.mu.Lock()
	s.connected = true
	s.mu.Unlock()
}

func (s *loginSession) isConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

func (s *loginSession) setCallClient(client *meowcaller.Client) {
	s.mu.Lock()
	s.callClient = client
	s.mu.Unlock()
}

// callCapableClient returns the session's meowcaller client, or nil when no call
// handler was attached for this number (no agent was assigned when it connected).
func (s *loginSession) callCapableClient() *meowcaller.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callClient
}

func (s *loginSession) setClosing() {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
}

func (s *loginSession) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

func qrDataURL(raw string) (string, error) {
	png, err := qrcode.Encode(raw, qrcode.Medium, 280)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

func clientJID(client *whatsmeow.Client) string {
	if client == nil || client.Store == nil || client.Store.ID == nil {
		return ""
	}
	return client.Store.ID.String()
}

func phoneNumberFromJID(jid types.JID) *string {
	user := strings.TrimSpace(jid.User)
	if user == "" {
		return nil
	}
	if !strings.HasPrefix(user, "+") {
		user = "+" + user
	}
	return &user
}

func normalizeDatabaseURL(databaseURL string) string {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return databaseURL
	}

	query := parsed.Query()
	if query.Has("schema") {
		query.Del("schema")
		parsed.RawQuery = query.Encode()
	}

	return parsed.String()
}
