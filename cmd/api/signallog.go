package main

import (
	"log"

	signallogger "go.mau.fi/libsignal/logger"
)

// signalLog sends libsignal's log lines through the standard logger, so they
// are silenced with everything else unless VERBOSE_LOGS is true. libsignal's
// own default logger prints straight to stdout, which put an [ERROR] line in
// the prod log for every "received message with old counter" — a message the
// previous instance already decrypted during a deploy overlap, which whatsmeow
// then drops. They are harmless.
type signalLog struct{}

func (signalLog) Debug(caller, msg string)   {}
func (signalLog) Info(caller, msg string)    { log.Printf("libsignal INFO %s: %s", caller, msg) }
func (signalLog) Warning(caller, msg string) { log.Printf("libsignal WARN %s: %s", caller, msg) }
func (signalLog) Error(caller, msg string)   { log.Printf("libsignal ERROR %s: %s", caller, msg) }
func (signalLog) Configure(string)           {}

func routeSignalLogs() {
	var l signallogger.Loggable = signalLog{}
	signallogger.Setup(&l)
}
