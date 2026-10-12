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
	case "reseal":
		return a.formsReseal(ctx, args[1:])
	case "web-key", "webkey":
		return a.formsWebKey(ctx, args[1:])
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
	fmt.Fprintln(a.stderr, "  s2u forms reseal    <formId>              make existing responses readable in the portal")
	fmt.Fprintln(a.stderr, "  s2u forms web-key   [--passphrase <p>]     set up in-browser reading (auto-generates a passphrase if omitted)")
	return 2
}

// formsWebKey sets up (or rotates with --force) the account's portal web key from the CLI,
// so the owner can read responses in the browser without doing the setup there (ADR-046
// Phase 2). The passphrase is auto-generated and printed when --passphrase is not given.
func (a app) formsWebKey(ctx context.Context, args []string) int {
	passphrase, force := "", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--passphrase", "-p":
			if i+1 >= len(args) {
				fmt.Fprintln(a.stderr, "--passphrase needs a value")
				return 2
			}
			i++
			passphrase = args[i]
		case "--force":
			force = true
		default:
			fmt.Fprintf(a.stderr, "unknown flag %q\n", args[i])
			return 2
		}
	}
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintln(a.stderr, "sign in first with `share2us login`")
		return 1
	}
	if _, exists, err := client.GetAccountWebKey(ctx); err != nil {
		return a.fail("check web key", err)
	} else if exists && !force {
		fmt.Fprintln(a.stderr, "a web key already exists. Use --force to replace it.")
		fmt.Fprintln(a.stderr, "Replacing it makes responses sealed to the old key unreadable in the portal until you run `s2u forms reseal <formId>`.")
		return 1
	}
	generated := passphrase == ""
	if generated {
		p, err := clicore.NewWebKeyPassphrase()
		if err != nil {
			return a.fail("generate passphrase", err)
		}
		passphrase = p
	}
	wk, err := clicore.GenerateWebKey(passphrase)
	if err != nil {
		return a.fail("generate web key", err)
	}
	if err := client.SaveWebKey(ctx, wk); err != nil {
		return a.fail("save web key", err)
	}
	fmt.Fprintln(a.stdout, "In-browser reading is set up.")
	if generated {
		fmt.Fprintf(a.stdout, "\n  Decrypt passphrase: %s\n\n", passphrase)
		fmt.Fprintln(a.stdout, "Save this now. Enter it in the portal to read responses in the browser.")
		fmt.Fprintln(a.stdout, "It is not stored anywhere and cannot be recovered.")
	}
	fmt.Fprintln(a.stdout, "Run `s2u forms reseal <formId>` to make existing responses readable in the portal.")
	return 0
}

// ensureWebKeyPublic returns the account's web public key, creating a web key (with an
// auto-generated passphrase, printed) if none exists yet. Used by reseal so the owner does
// not have to set the key up separately first.
func (a app) ensureWebKeyPublic(ctx context.Context, client *clicore.Client) (string, int) {
	wk, exists, err := client.GetAccountWebKey(ctx)
	if err != nil {
		return "", a.fail("get web key", err)
	}
	if exists {
		return wk.PublicKey, 0
	}
	passphrase, err := clicore.NewWebKeyPassphrase()
	if err != nil {
		return "", a.fail("generate passphrase", err)
	}
	created, err := clicore.GenerateWebKey(passphrase)
	if err != nil {
		return "", a.fail("generate web key", err)
	}
	if err := client.SaveWebKey(ctx, created); err != nil {
		return "", a.fail("save web key", err)
	}
	fmt.Fprintln(a.stdout, "No portal web key was set up, so one was created for you.")
	fmt.Fprintf(a.stdout, "\n  Decrypt passphrase: %s\n\n", passphrase)
	fmt.Fprintln(a.stdout, "Save this now. Enter it in the portal to read responses in the browser.")
	fmt.Fprintln(a.stdout, "It is not stored anywhere and cannot be recovered.")
	return created.PublicKey, 0
}

// formsReseal makes a form's existing responses readable in the portal (ADR-046 Phase 2):
// it decrypts each response's content key with this device's key and re-seals it to the
// owner's portal web key. Responses submitted before the web key existed are not sealed to
// it; this backfills them. Already-resealed responses are skipped.
func (a app) formsReseal(ctx context.Context, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(a.stderr, "usage: s2u forms reseal <formId>")
		return 2
	}
	formID := args[0]
	client, cred, ok := a.authClient()
	if !ok {
		fmt.Fprintln(a.stderr, "sign in first with `share2us login`")
		return 1
	}
	if cred.DevicePrivateKey == "" || cred.DevicePublicKey == "" {
		fmt.Fprintln(a.stderr, "this login has no device key, so it cannot decrypt responses to re-seal them.")
		return 1
	}
	webPubKey, code := a.ensureWebKeyPublic(ctx, client)
	if code != 0 {
		return code
	}

	var seals []clicore.ResponseWebSeal
	skipped, failed, offset := 0, 0, 0
	for {
		rows, hasMore, err := client.ListFormResponses(ctx, formID, 200, offset)
		if err != nil {
			return a.fail("list form responses", err)
		}
		for _, row := range rows {
			payload, err := client.GetFormResponse(ctx, formID, row.ID)
			if err != nil {
				failed++
				continue
			}
			if payload.WebSealedKey != "" {
				skipped++
				continue
			}
			var contentKey []byte
			for _, sk := range payload.SealedKeys {
				if ck, err := clicore.OpenSealedContentKey(sk.SealedKey, cred.DevicePublicKey, cred.DevicePrivateKey); err == nil {
					contentKey = ck
					break
				}
			}
			if contentKey == nil {
				failed++
				continue
			}
			webSealed, err := clicore.SealContentKeyForDevice(contentKey, webPubKey)
			if err != nil {
				failed++
				continue
			}
			seals = append(seals, clicore.ResponseWebSeal{ResponseID: row.ID, SealedKey: webSealed})
		}
		if !hasMore {
			break
		}
		offset += len(rows)
	}

	if len(seals) == 0 {
		fmt.Fprintf(a.stdout, "Nothing to re-seal (%d already readable in the portal, %d could not be decrypted on this device).\n", skipped, failed)
		return 0
	}
	n, err := client.ResealFormResponsesWeb(ctx, formID, seals)
	if err != nil {
		return a.fail("re-seal responses", err)
	}
	fmt.Fprintf(a.stdout, "Re-sealed %d response(s) to your portal web key (%d already readable, %d could not be decrypted on this device).\n", n, skipped, failed)
	return 0
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
