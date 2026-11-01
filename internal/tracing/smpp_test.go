package tracing_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ellanetworks/simota/internal/tracing"
	"github.com/ellanetworks/smsc/smpp"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestEndSMPP checks the outcome that the span of an SMPP operation ends with: its response's command_status, and the
// error.type of a failure, as Ella SMSC gives them.
func TestEndSMPP(t *testing.T) {
	ok, appn := smpp.StatusOK, smpp.StatusXTAppn

	for _, tc := range []struct {
		name            string
		status          *smpp.CommandStatus
		err             error
		code, errorType string
	}{
		{"ok", &ok, nil, "ESME_ROK", ""},
		{
			"refused", nil, fmt.Errorf("submit: %w", &smpp.StatusError{Command: smpp.CmdSubmitSMResp, Status: smpp.StatusThrottled}),
			"ESME_RTHROTTLED", "ESME_RTHROTTLED",
		},
		{"answered an error", &appn, nil, "ESME_RX_T_APPN", "ESME_RX_T_APPN"},
		{"answered an error for a cause", &appn, errors.New("database is locked"), "ESME_RX_T_APPN", "ESME_RX_T_APPN"},
		{"answer not written", &ok, fmt.Errorf("%w: reset", smpp.ErrClosed), "ESME_ROK", "session_closed"},
		{"no session", nil, smpp.ErrNoSession, "", "no_session"},
		{"closed", nil, fmt.Errorf("%w: reset", smpp.ErrClosed), "", "session_closed"},
		{"response timer", nil, smpp.ErrResponseTimeout, "", "timeout"},
		{"deadline", nil, context.DeadlineExceeded, "", "timeout"},
		{"canceled", nil, context.Canceled, "", "canceled"},
		{"other", nil, errors.New("broken"), "", "_OTHER"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tracetest.NewSpanRecorder()
			_, span := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)).Tracer("test").Start(t.Context(), "op")

			tracing.EndSMPP(span, tc.status, tc.err)

			ended := rec.Ended()
			if len(ended) != 1 {
				t.Fatalf("ended %d spans", len(ended))
			}

			got := map[string]string{}
			for _, kv := range ended[0].Attributes() {
				got[string(kv.Key)] = kv.Value.String()
			}

			wantStatus := codes.Unset
			if tc.errorType != "" {
				wantStatus = codes.Error
			}

			if got["rpc.response.status_code"] != tc.code || got["error.type"] != tc.errorType ||
				ended[0].Status().Code != wantStatus {
				t.Fatalf("attributes %v status %v", got, ended[0].Status())
			}
		})
	}
}
