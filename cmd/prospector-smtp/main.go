// prospector-smtp proves the outbound mail configuration before any real message depends on it.
//
// Two modes, deliberately separate:
//
//	-verify (default)  dial, STARTTLS, authenticate, quit. Sends nothing, contacts nobody.
//	-send -to ADDRESS  one complete message to an address you name explicitly.
//
// The send mode requires -to with a real address and will not read a destination from
// configuration, because "send a test email" should never be able to pick its own recipient. It
// refuses any address that is not a human mailbox, and it is not connected to the prospect
// pipeline in any way: it composes its own fixed body and touches no draft, plan or contact.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/relevance"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/joho/godotenv"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("prospector-smtp", flag.ContinueOnError)
	doSend := flags.Bool("send", false, "actually transmit one test message (requires -to)")
	to := flags.String("to", "", "destination for the test message; must be an address you own")
	out := flags.String("out", "", "write the result as JSON here")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return errors.New("could not load environment file")
	}

	transport, reason := send.NewSMTPFromEnv()
	if reason != "" {
		return fmt.Errorf("SMTP is not usable: %s", reason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if !*doSend {
		result := transport.Verify(ctx)
		printVerify(result)
		if *out != "" {
			if err := writeJSON(*out, result); err != nil {
				return err
			}
		}
		if !result.OK {
			return fmt.Errorf("verification failed at stage %s", result.Stage)
		}
		return nil
	}

	address := strings.TrimSpace(*to)
	if address == "" {
		return errors.New("-to is required for -send: name the address you own, explicitly")
	}
	if endpoint := relevance.ClassifyEndpoint(address); !endpoint.Contactable {
		return fmt.Errorf("refusing to send to %s: %s", address, endpoint.Evidence)
	}
	// A fixed, obviously-a-test body. Nothing from the prospect pipeline is involved, so this
	// cannot accidentally transmit a draft.
	message := send.Message{
		ToAddress: address,
		Subject:   "Prospector SMTP smoke test",
		Body: "This is an operator-initiated SMTP smoke test from Prospector.\n\n" +
			"It verifies authentication, TLS, the From and Reply-To headers, the Message-ID domain, " +
			"plain-text delivery and provider acceptance.\n\n" +
			"No prospect, draft, contact plan or candidate profile was involved in sending it.\n",
		MessageID: send.NewMessageID(fmt.Sprintf("smoketest-%d", time.Now().UTC().UnixNano()), transport.SenderDomain()),
	}
	fmt.Printf("Sending one test message to %s via %s as %s\n", address, transport.Host, transport.FromAddress)
	result := transport.Send(ctx, message)
	printSend(result, message)
	if *out != "" {
		if err := writeJSON(*out, map[string]any{"result": result, "message_id": message.MessageID,
			"to": address, "from": transport.FromAddress, "reply_to": transport.ReplyTo}); err != nil {
			return err
		}
	}
	if result.Outcome != send.OutcomeAccepted {
		return fmt.Errorf("smoke test was not accepted: %s at stage %s (%s)",
			result.Outcome, result.Stage, result.Response)
	}
	return nil
}

func printVerify(r send.VerifyResult) {
	fmt.Println("SMTP configuration verification — no message was sent, no recipient was contacted")
	fmt.Printf("  reached stage      %s\n", r.Stage)
	fmt.Printf("  STARTTLS           %v\n", r.STARTTLS)
	if r.TLSVersion != "" {
		fmt.Printf("  TLS                %s / %s\n", r.TLSVersion, r.TLSCipher)
	}
	fmt.Printf("  authenticated      %v\n", r.Authed)
	fmt.Printf("  From               %s\n", r.From)
	fmt.Printf("  Reply-To           %s\n", orUnset(r.ReplyTo))
	fmt.Printf("  Message-ID domain  %s\n", orUnset(r.MessageIDAt))
	if r.Error != "" {
		fmt.Printf("  error              %s\n", r.Error)
	}
	if r.OK {
		fmt.Println("  RESULT: the server accepted these credentials over TLS.")
		fmt.Println("  Receipt in a real inbox is still unproven; that needs -send -to <your address>.")
	} else {
		fmt.Println("  RESULT: not usable for sending.")
	}
}

func printSend(r send.Result, m send.Message) {
	fmt.Printf("  outcome        %s\n", r.Outcome)
	fmt.Printf("  stage reached  %s\n", r.Stage)
	fmt.Printf("  provider       %s\n", r.Provider)
	fmt.Printf("  response       %s\n", r.Response)
	fmt.Printf("  Message-ID     %s\n", m.MessageID)
	if r.ErrorCategory != "" {
		fmt.Printf("  error category %s\n", r.ErrorCategory)
	}
	if r.Outcome == send.OutcomeAccepted {
		fmt.Println("  RESULT: the provider accepted the message. Confirm it arrived in the inbox,")
		fmt.Println("  and check the From, Reply-To and Message-ID headers on the received copy.")
	}
}

func orUnset(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(not set)"
	}
	return value
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
