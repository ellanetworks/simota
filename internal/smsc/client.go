// Package smsc submits binary short messages to Ella SMSC over its API, and reads back their status and the
// SMS-DELIVER-REPORTs that their recipients answered with.
package smsc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// ProtocolUSIMDataDownload is the TP-PID of (U)SIM data download (TS 23.040 clause 9.2.3.9).
	ProtocolUSIMDataDownload = 0x7f
	// DCSClass2 is the TP-DCS of 8-bit data of message class 2, (U)SIM specific (TS 23.038 clause 4).
	DCSClass2 = 0xf6
)

// Client is a client of the API of Ella SMSC, with an API token of a user that may submit messages.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// Status is the delivery status of a message.
type Status string

const (
	StatusPending   Status = "pending"
	StatusDelivered Status = "delivered"
	StatusFailed    Status = "failed"
	StatusExpired   Status = "expired"
)

type Message struct {
	ID     int64  `json:"id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Status Status `json:"status"`
}

// DeliverReport is an SMS-DELIVER-REPORT of a delivery attempt. Its user data, here the PoR, is absent when the
// serving node passed none on.
type DeliverReport struct {
	AttemptID      int64 `json:"attempt_id"`
	UserDataHeader Hex   `json:"user_data_header"`
	UserData       Hex   `json:"user_data"`
}

// Binary is a short message whose TPDU parameters the caller codes.
type Binary struct {
	ProtocolIdentifier uint8 `json:"protocol_identifier"`
	DataCodingScheme   uint8 `json:"data_coding_scheme"`
	UserDataHeader     Hex   `json:"user_data_header,omitempty"`
	UserData           Hex   `json:"user_data"`
}

// Hex is octets that JSON carries as a hexadecimal string.
type Hex []byte

func (h Hex) MarshalJSON() ([]byte, error) {
	return json.Marshal(hex.EncodeToString(h))
}

func (h *Hex) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}

	v, err := hex.DecodeString(s)
	if err != nil {
		return err
	}

	*h = v

	return nil
}

// SubmitBinary submits a binary message to the MSISDN to, from the address from, and returns its id.
func (c *Client) SubmitBinary(ctx context.Context, from, to string, b Binary) (int64, error) {
	body := struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Binary Binary `json:"binary"`
	}{from, to, b}

	var res struct {
		Items []Message `json:"items"`
	}

	if err := c.do(ctx, http.MethodPost, "/api/v1/messages", body, &res); err != nil {
		return 0, fmt.Errorf("submit: %w", err)
	}

	if len(res.Items) != 1 {
		return 0, fmt.Errorf("submit: %d messages queued, want 1", len(res.Items))
	}

	return res.Items[0].ID, nil
}

func (c *Client) Message(ctx context.Context, id int64) (Message, error) {
	var m Message
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", id), nil, &m); err != nil {
		return Message{}, fmt.Errorf("message %d: %w", id, err)
	}

	return m, nil
}

// DeliverReports are the SMS-DELIVER-REPORTs of message id, oldest first.
func (c *Client) DeliverReports(ctx context.Context, id int64) ([]DeliverReport, error) {
	var res struct {
		DeliverReports []DeliverReport `json:"deliver_reports"`
	}

	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/content", id), nil, &res); err != nil {
		return nil, fmt.Errorf("message %d content: %w", id, err)
	}

	return res.DeliverReports, nil
}

// Wait polls message id every interval until it is no longer pending.
func (c *Client) Wait(ctx context.Context, id int64, interval time.Duration) (Message, error) {
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		m, err := c.Message(ctx, id)
		if err != nil {
			return Message{}, err
		}

		if m.Status != StatusPending {
			return m, nil
		}

		select {
		case <-ctx.Done():
			return m, ctx.Err()
		case <-t.C:
		}
	}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader

	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}

		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.Token)

	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}

	resp, err := hc.Do(req)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}

		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s: %s", resp.Status, e.Error)
		}

		return errors.New(resp.Status)
	}

	var env struct {
		Result json.RawMessage `json:"result"`
	}

	if err := json.Unmarshal(b, &env); err != nil {
		return err
	}

	return json.Unmarshal(env.Result, out)
}
