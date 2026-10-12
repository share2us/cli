package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	clicore "github.com/share2us/cli-core"
)

// forms reads responses to a form share (ADR-046). Forms are authored by uploading an
// .mdx form and filled in the portal; this command is the Phase-1 home for reading the
// end-to-end-encrypted responses, which are decrypted locally with this device's key.
func (a app) forms(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return a.formsUsage()
	}
	switch args[0] {
	case "responses", "list":
		return a.formsResponses(ctx, args[1:])
	case "response", "open", "read":
		return a.formsResponse(ctx, args[1:])
	case "help", "-h", "--help":
		return a.formsUsage()
	default:
		fmt.Fprintf(a.stderr, "unknown forms command %q\n", args[0])
		return a.formsUsage()
	}
}

func (a app) formsUsage() int {
	fmt.Fprintln(a.stderr, "usage:")
	fmt.Fprintln(a.stderr, "  s2u forms responses <formId>              list submissions (owner only)")
	fmt.Fprintln(a.stderr, "  s2u forms response  <formId> <responseId> decrypt and show one submission")
	return 2
}

func (a app) formsResponses(ctx context.Context, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(a.stderr, "usage: s2u forms responses <formId>")
		return 2
	}
	formID := args[0]
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintln(a.stderr, "sign in first with `share2us login`")
		return 1
	}
	rows, hasMore, err := client.ListFormResponses(ctx, formID, 200, 0)
	if err != nil {
		return a.fail("list form responses", err)
	}
	if len(rows) == 0 {
		fmt.Fprintln(a.stdout, "No responses yet.")
		return 0
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "RESPONSE ID\tSUBMITTER\tSTATUS\tSUBMITTED")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.ID, orDash(r.SubmitterEmail), r.Status, r.CreatedAt.Local().Format(time.RFC822))
	}
	tw.Flush()
	if hasMore {
		fmt.Fprintln(a.stdout, "(more responses exist; this shows the first 200)")
	}
	fmt.Fprintf(a.stdout, "\nRead one with: s2u forms response %s <responseId>\n", formID)
	return 0
}

func (a app) formsResponse(ctx context.Context, args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(a.stderr, "usage: s2u forms response <formId> <responseId>")
		return 2
	}
	formID, responseID := args[0], args[1]
	client, cred, ok := a.authClient()
	if !ok {
		fmt.Fprintln(a.stderr, "sign in first with `share2us login`")
		return 1
	}
	if cred.DevicePrivateKey == "" || cred.DevicePublicKey == "" {
		fmt.Fprintln(a.stderr, "this login has no device key, so it cannot decrypt responses.")
		fmt.Fprintln(a.stderr, "sign in on a device (not a CI token) with `share2us login`.")
		return 1
	}
	payload, err := client.GetFormResponse(ctx, formID, responseID)
	if err != nil {
		return a.fail("get form response", err)
	}
	if len(payload.SealedKeys) == 0 {
		fmt.Fprintln(a.stderr, "no key for this device is attached to this response.")
		fmt.Fprintln(a.stderr, "open it on the device that can read it (the owner's, or the one that submitted it).")
		return 1
	}
	// The server returns the sealed keys for this account's devices. Only the one
	// sealed to THIS device's key will open, so try each and use the first that works.
	var answers []byte
	for _, sk := range payload.SealedKeys {
		plain, err := clicore.OpenFormResponse(payload.Ciphertext, sk.SealedKey, cred.DevicePublicKey, cred.DevicePrivateKey)
		if err == nil {
			answers = plain
			break
		}
	}
	if answers == nil {
		fmt.Fprintln(a.stderr, "could not decrypt this response with this device's key.")
		fmt.Fprintln(a.stderr, "it may be sealed to another of your devices; try there.")
		return 1
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, answers, "", "  ") == nil {
		fmt.Fprintln(a.stdout, pretty.String())
	} else {
		fmt.Fprintln(a.stdout, string(answers))
	}
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
