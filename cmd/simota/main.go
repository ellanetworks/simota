// Command simota sends Remote File Management scripts to a SIM over Ella SMSC, and prints the card's answer.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/simota/internal/ota"
	"github.com/ellanetworks/simota/internal/rfm"
	"github.com/ellanetworks/simota/internal/smsc"
)

const usage = `usage: simota [flags] <command>
       simota serve [-addr 127.0.0.1:5020] [-db simota.db]

commands:
  oplmn read                       read EF.OPLMNwAcT
  oplmn set <slot> <mcc> <mnc>     write a slot of EF.OPLMNwAcT, then read the file
  oplmn clear <slot>               empty a slot of EF.OPLMNwAcT, then read the file
  script <hex>                     send a compact command script as is

The SMSC URL and API token come from SIMOTA_SMSC_URL and SIMOTA_SMSC_TOKEN. With -card, the MSISDN, TAR and key
set come from the card's settings in the database.

flags:
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "simota:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		return serve(os.Args[2:])
	}

	fs := flag.NewFlagSet("simota", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}

	to := fs.String("to", "", "MSISDN of the card, E.164")
	from := fs.String("from", "+15550001000", "originating address of the OTA messages")
	kic := fs.String("kic", "", "KIc of the key set, hex")
	kid := fs.String("kid", "", "KID of the key set, hex")
	kvn := fs.Uint("kvn", 1, "key version number of the key set")
	tar := fs.String("tar", "b00010", "TAR of the RFM application, hex")
	access := fs.String("access", "eutran,ngran", "access technologies of a set entry: utran, eutran, ngran, gsm")
	timeout := fs.Duration("timeout", 2*time.Minute, "how long to wait for the delivery")
	card := fs.String("card", "", "ICCID of a card in the database, instead of -to, -kic, -kid, -kvn and -tar")
	dbPath := fs.String("db", "simota.db", "path to the database, with -card")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var (
		keyset ota.Keyset
		t      []byte
		err    error
	)

	if *card != "" {
		c, k, err := cardSettings(ctx, *dbPath, *card)
		if err != nil {
			return err
		}

		if c.MSISDN == "" {
			return fmt.Errorf("card %s has no MSISDN", c.ICCID)
		}

		keyset = ota.Keyset{Version: uint8(k.Version), Algorithm: ota.TripleDES, KIc: k.KIc, KID: k.KID}
		if k.Algorithm == "aes" {
			keyset.Algorithm = ota.AES
		}

		*to, t = c.MSISDN, c.TAR
	} else {
		if keyset, err = parseKeyset(*kvn, *kic, *kid); err != nil {
			return err
		}

		if t, err = hex.DecodeString(*tar); err != nil || len(t) != 3 {
			return fmt.Errorf("tar %q is not 3 octets of hex", *tar)
		}
	}

	if *to == "" {
		return errors.New("-to is required")
	}

	script, err := buildScript(fs.Args(), *access)
	if err != nil {
		return err
	}

	c := &smsc.Client{BaseURL: os.Getenv("SIMOTA_SMSC_URL"), Token: os.Getenv("SIMOTA_SMSC_TOKEN")}
	if c.BaseURL == "" || c.Token == "" {
		return errors.New("SIMOTA_SMSC_URL and SIMOTA_SMSC_TOKEN are required")
	}

	cmd := ota.Command{
		Keyset: keyset,
		SPI: ota.SPI{
			Integrity:    ota.IntegrityCC,
			Ciphering:    true,
			PoR:          ota.PoRAlways,
			PoRIntegrity: ota.IntegrityCC,
			PoRCiphering: true,
		},
		Data: script,
	}
	copy(cmd.TAR[:], t)

	return send(ctx, os.Stdout, c, cmd, *from, *to, fs.Arg(0) == "oplmn")
}

func parseKeyset(kvn uint, kic, kid string) (ota.Keyset, error) {
	ic, err := hex.DecodeString(kic)
	if err != nil {
		return ota.Keyset{}, fmt.Errorf("kic: %w", err)
	}

	id, err := hex.DecodeString(kid)
	if err != nil {
		return ota.Keyset{}, fmt.Errorf("kid: %w", err)
	}

	if kvn > 15 {
		return ota.Keyset{}, fmt.Errorf("kvn %d not in 1-15", kvn)
	}

	return ota.Keyset{Version: uint8(kvn), Algorithm: ota.TripleDES, KIc: ic, KID: id}, nil
}

func buildScript(args []string, access string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("no command; see -h")
	}

	readFile := func(s *rfm.Script) *rfm.Script {
		return s.ReadBinary(0, 0)
	}

	oplmn := func() *rfm.Script {
		return rfm.NewScript(rfm.ClassGSM).Select(rfm.FileDFGSM).Select(rfm.FileOPLMNwAcT)
	}

	switch {
	case args[0] == "script" && len(args) == 2:
		return hex.DecodeString(args[1])
	case args[0] != "oplmn" || len(args) < 2:
		return nil, errors.New("unknown command; see -h")
	case args[1] == "read" && len(args) == 2:
		return readFile(oplmn()).Bytes()
	case args[1] == "set" && len(args) == 5:
		slot, err := parseSlot(args[2])
		if err != nil {
			return nil, err
		}

		a, err := parseAccess(access)
		if err != nil {
			return nil, err
		}

		e, err := rfm.SelectorEntry{MCC: args[3], MNC: args[4], Access: a}.Encode()
		if err != nil {
			return nil, err
		}

		return readFile(oplmn().UpdateBinary(rfm.SlotOffset(slot), e)).Bytes()
	case args[1] == "clear" && len(args) == 3:
		slot, err := parseSlot(args[2])
		if err != nil {
			return nil, err
		}

		e, _ := rfm.Empty.Encode()

		return readFile(oplmn().UpdateBinary(rfm.SlotOffset(slot), e)).Bytes()
	default:
		return nil, errors.New("unknown command; see -h")
	}
}

func parseSlot(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 255/5 {
		return 0, fmt.Errorf("invalid slot %q", s)
	}

	return n, nil
}

func parseAccess(s string) (rfm.AccessTechnology, error) {
	names := map[string]rfm.AccessTechnology{"utran": rfm.UTRAN, "eutran": rfm.EUTRAN, "ngran": rfm.NGRAN, "gsm": rfm.GSM}

	var a rfm.AccessTechnology

	for _, n := range strings.Split(s, ",") {
		t, ok := names[strings.TrimSpace(n)]
		if !ok {
			return 0, fmt.Errorf("unknown access technology %q", n)
		}

		a |= t
	}

	return a, nil
}

func send(ctx context.Context, out io.Writer, c *smsc.Client, cmd ota.Command, from, to string, selector bool) error {
	udh, ud, err := cmd.Encode()
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "command packet: %x %x\n", udh, ud)

	id, err := c.SubmitBinary(ctx, from, to, smsc.Binary{
		ProtocolIdentifier: smsc.ProtocolUSIMDataDownload,
		DataCodingScheme:   smsc.DCSClass2,
		UserDataHeader:     udh,
		UserData:           ud,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "message %d queued\n", id)

	m, err := c.Wait(ctx, id, time.Second)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "message %d %s\n", id, m.Status)

	reports, err := c.DeliverReports(ctx, id)
	if err != nil {
		return err
	}

	if len(reports) == 0 || len(reports[len(reports)-1].UserData) == 0 {
		return errors.New("no PoR: the delivery report carries no user data")
	}

	last := reports[len(reports)-1]
	fmt.Fprintf(out, "response packet: %x %x\n", last.UserDataHeader, last.UserData)

	r, err := ota.DecodeResponse(cmd.Keyset, cmd.SPI, last.UserDataHeader, last.UserData)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "PoR: %s, TAR %x", r.Status, r.TAR)

	if r.Unsecured {
		fmt.Fprint(out, ", unsecured")
	}

	fmt.Fprintln(out)

	if r.Status != ota.StatusPoROK {
		return fmt.Errorf("card refused the command: %s", r.Status)
	}

	rr, err := rfm.DecodeResponse(r.Data)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "script: %d commands executed, last status word %04x, data %x\n", rr.Executed, rr.StatusWord, rr.Data)

	if !rr.OK() {
		return fmt.Errorf("command %d failed with status word %04x", rr.Executed, rr.StatusWord)
	}

	if selector {
		entries, err := rfm.DecodeSelector(rr.Data)
		if err != nil {
			return err
		}

		for i, e := range entries {
			if e == rfm.Empty {
				fmt.Fprintf(out, "  %2d  -\n", i+1)
				continue
			}

			fmt.Fprintf(out, "  %2d  %s/%s  %s\n", i+1, e.MCC, e.MNC, e.Access)
		}
	}

	return nil
}
