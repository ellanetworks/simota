package tracing

import (
	"context"
	"errors"
	"strings"

	"github.com/ellanetworks/smsc/smpp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// RPCSystemSMPP is the rpc.system.name of the spans of SMPP operations, as Ella SMSC's are: SMPP is no RPC system
// that the conventions name, and they let a system name its own.
const RPCSystemSMPP = "smpp"

// SMPPAttributes are the attributes of the span of SMPP operation id, which the span is named after (OpenTelemetry
// semantic conventions, RPC spans).
func SMPPAttributes(id smpp.CommandID) []attribute.KeyValue {
	return []attribute.KeyValue{semconv.RPCSystemNameKey.String(RPCSystemSMPP), semconv.RPCMethod(id.String())}
}

// EndSMPP ends the span of an SMPP operation: with rpc.response.status_code the command_status of its response, when
// there is one, which err carries when the peer refused the request; and as an error, of the low-cardinality
// error.type that Ella SMSC also gives, when status is not ESME_ROK or err tells another failure. For a server's
// operation, status is its answer, and err, if any, why it answered so, or why its answer could not be written.
func EndSMPP(span trace.Span, status *smpp.CommandStatus, err error) {
	defer span.End()

	var se *smpp.StatusError
	if errors.As(err, &se) {
		status = &se.Status
	}

	if status != nil {
		span.SetAttributes(semconv.RPCResponseStatusCode(status.String()))
	}

	var (
		stateErr *smpp.StateError
		errType  string
	)

	switch {
	case status != nil && *status != smpp.StatusOK:
		errType = statusErrorType(*status)
	case errors.Is(err, smpp.ErrNoSession):
		errType = "no_session"
	case errors.Is(err, smpp.ErrClosed) || errors.As(err, &stateErr):
		errType = "session_closed"
	case errors.Is(err, smpp.ErrResponseTimeout) || errors.Is(err, context.DeadlineExceeded):
		errType = "timeout"
	case errors.Is(err, context.Canceled):
		errType = "canceled"
	case err != nil:
		errType = semconv.ErrorTypeOther.Value.AsString()
	default:
		return
	}

	span.SetAttributes(semconv.ErrorTypeKey.String(errType))

	if err != nil {
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Error, "")
	}
}

// statusErrorType is the error.type of a command_status: its name, or _OTHER for one that SMPP 3.4 does not name,
// which a peer chooses.
func statusErrorType(status smpp.CommandStatus) string {
	if name := status.String(); strings.HasPrefix(name, "ESME_") {
		return name
	}

	return semconv.ErrorTypeOther.Value.AsString()
}
