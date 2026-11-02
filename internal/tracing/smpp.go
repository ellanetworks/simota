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

const RPCSystemSMPP = "smpp"

func SMPPAttributes(id smpp.CommandID) []attribute.KeyValue {
	return []attribute.KeyValue{semconv.RPCSystemNameKey.String(RPCSystemSMPP), semconv.RPCMethod(id.String())}
}

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

func statusErrorType(status smpp.CommandStatus) string {
	if name := status.String(); strings.HasPrefix(name, "ESME_") {
		return name
	}

	return semconv.ErrorTypeOther.Value.AsString()
}
