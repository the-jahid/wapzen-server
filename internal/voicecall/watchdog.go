package voicecall

import (
	"log"
	"sync"
	"time"
)

// Defaults for the call watchdog. Each has an env override; 0 turns that
// check off.
const (
	// defaultIdlePromptSeconds is how long both sides may be silent before the
	// agent asks, once, whether the caller is still there.
	defaultIdlePromptSeconds = 20
	// defaultIdleHangupSeconds is how long the line may stay silent after that
	// question before the agent says goodbye and hangs up.
	defaultIdleHangupSeconds = 20
	// defaultMaxCallMinutes caps one call. A forgotten open line otherwise runs
	// a paid Realtime session for as long as WhatsApp keeps the call up.
	defaultMaxCallMinutes = 20
)

// What the agent is asked to say at each watchdog step. The session
// instructions (language lock included) ride along with each request, so these
// are spoken in the agent's language and voice.
const (
	idlePromptInstruction  = "The caller has been silent for a while. Ask once, briefly and politely, whether they are still on the line. Say nothing else."
	idleGoodbyeInstruction = "The caller has not responded. Politely say that you will end the call now because you cannot hear them, thank them for calling and say goodbye, in one or two short sentences."
	timeLimitInstruction   = "This call has reached its maximum length. Politely tell the caller you need to end the call now, thank them and say goodbye, in one or two short sentences."
)

type watchdogConfig struct {
	idlePrompt  time.Duration
	idleHangup  time.Duration
	maxDuration time.Duration
	// minutesCapped says maxDuration is the account's remaining voice minutes
	// rather than the server's call-length cap, which changes the recorded
	// end reason.
	minutesCapped bool
}

// withMinutesLeft shortens the call-length cap to the account's remaining
// voice minutes when those run out first. left <= 0 means no account limit.
func (cfg watchdogConfig) withMinutesLeft(left time.Duration) watchdogConfig {
	if left <= 0 {
		return cfg
	}
	if cfg.maxDuration <= 0 || left < cfg.maxDuration {
		cfg.maxDuration = left
		cfg.minutesCapped = true
	}
	return cfg
}

// limitReason is the end reason recorded when the call hits its length cap.
func (cfg watchdogConfig) limitReason() string {
	if cfg.minutesCapped {
		return "voice minutes used up"
	}
	return "maximum call length reached"
}

func loadWatchdogConfig() watchdogConfig {
	return watchdogConfig{
		idlePrompt:  time.Duration(envInt("VOICE_IDLE_PROMPT_SECONDS", defaultIdlePromptSeconds)) * time.Second,
		idleHangup:  time.Duration(envInt("VOICE_IDLE_HANGUP_SECONDS", defaultIdleHangupSeconds)) * time.Second,
		maxDuration: time.Duration(envInt("VOICE_MAX_CALL_MINUTES", defaultMaxCallMinutes)) * time.Minute,
	}
}

// callWatchdog watches one live call for silence and for its length. It only
// decides when; what happens is done through the callbacks run hands it, so it
// stays testable without a call on the wire.
type callWatchdog struct {
	cfg watchdogConfig
	now func() time.Time

	mu        sync.Mutex
	startedAt time.Time
	last      time.Time
	nudged    bool
	finished  bool
}

func newCallWatchdog(cfg watchdogConfig) *callWatchdog {
	return &callWatchdog{cfg: cfg, now: time.Now}
}

// touch records activity on the line: the caller talking, or the agent's audio
// starting or finishing.
func (w *callWatchdog) touch() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.last = w.now()
	w.mu.Unlock()
}

// callerSpoke records the caller talking, which also re-arms the "are you
// still there?" question for the next silence.
func (w *callWatchdog) callerSpoke() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.last = w.now()
	w.nudged = false
	w.mu.Unlock()
}

// watchdogAction is what one check decided.
type watchdogAction int

const (
	watchdogNone watchdogAction = iota
	watchdogAskStillThere
	watchdogIdleGoodbye
	watchdogTimeLimit
)

// check decides what, if anything, the call needs now. busy reports the agent
// speaking or generating a reply, which is never silence.
func (w *callWatchdog) check(busy bool) watchdogAction {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.finished || w.startedAt.IsZero() {
		return watchdogNone
	}
	now := w.now()
	if w.cfg.maxDuration > 0 && now.Sub(w.startedAt) >= w.cfg.maxDuration {
		w.finished = true
		return watchdogTimeLimit
	}
	if busy {
		w.last = now
		return watchdogNone
	}
	idle := now.Sub(w.last)
	if !w.nudged {
		if w.cfg.idlePrompt > 0 && idle >= w.cfg.idlePrompt {
			return watchdogAskStillThere
		}
		return watchdogNone
	}
	if w.cfg.idleHangup > 0 && idle >= w.cfg.idleHangup {
		w.finished = true
		return watchdogIdleGoodbye
	}
	return watchdogNone
}

// markNudged records that the still-there question was actually asked; a
// request refused because a reply was in flight is simply retried next tick.
func (w *callWatchdog) markNudged() {
	w.mu.Lock()
	w.nudged = true
	w.last = w.now()
	w.mu.Unlock()
}

// start begins watching from the moment the call is live and runs until stop
// is closed. speak asks the agent to say something (reporting whether the
// request went out); end arms the release after the agent's closing line.
func (w *callWatchdog) start(callID string, stop <-chan struct{}, busy func() bool, speak func(string) bool, end func(string)) {
	if w == nil {
		return
	}
	w.mu.Lock()
	if !w.startedAt.IsZero() {
		w.mu.Unlock()
		return
	}
	w.startedAt = w.now()
	w.last = w.startedAt
	w.mu.Unlock()

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			switch w.check(busy()) {
			case watchdogAskStillThere:
				if speak(idlePromptInstruction) {
					log.Printf("voicecall: call %s silent for %s; asking whether the caller is still there", callID, w.cfg.idlePrompt)
					w.markNudged()
				}
			case watchdogIdleGoodbye:
				log.Printf("voicecall: call %s still silent; ending it", callID)
				end("caller stopped responding")
				speak(idleGoodbyeInstruction)
				return
			case watchdogTimeLimit:
				log.Printf("voicecall: call %s reached the %s limit; ending it", callID, w.cfg.maxDuration)
				end(w.cfg.limitReason())
				speak(timeLimitInstruction)
				return
			}
		}
	}()
}
