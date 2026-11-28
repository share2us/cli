package main

// Secure ShareNet on the CLI (ADR-051 option A, milestone 3): `s2u keys` manages this
// account's end-to-end key and this device's access to it; `s2u sharenet files|get` reads
// sharenet files (decrypting Secure ones); uploads into a Secure ShareNet are encrypted in
// upload() via prepareSecurePost.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	clicore "github.com/share2us/cli-core"
)

// secureSession is what every Secure ShareNet operation needs on this device.
func (a app) secureSession(ctx context.Context) (*clicore.SecureNet, clicore.Credential, error) {
	return a.secureSessionMode(ctx, false)
}

// secureSessionMode: unattended (the daemon) never accepts a key on first sight.
func (a app) secureSessionMode(ctx context.Context, unattended bool) (*clicore.SecureNet, clicore.Credential, error) {
	client, credential, ok := a.authClient()
	if !ok {
		return nil, credential, fmt.Errorf("not logged in; run `%s login`", commandName)
	}
	if clicore.IsAPIToken(credential.Token) || credential.DevicePrivateKey == "" {
		return nil, credential, errors.New("Secure ShareNet keys live on a signed-in device; a personal API token can't hold them")
	}
	accountID := credential.AccountID
	if accountID == "" {
		me, err := client.Me(ctx)
		if err != nil {
			return nil, credential, err
		}
		accountID = me.AccountID
	}
	kr, err := clicore.NewKeyring(client, accountID, credential.DevicePrivateKey)
	if err != nil {
		return nil, credential, err
	}
	pinPath, err := clicore.PinStorePath()
	if err != nil {
		return nil, credential, err
	}
	pins, err := clicore.OpenPinStore(pinPath)
	if err != nil {
		return nil, credential, err
	}
	return &clicore.SecureNet{Keys: kr, Pins: pins, Unattended: unattended}, credential, nil
}

func deviceLabel() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "s2u"
	}
	if len(h) > 100 {
		h = h[:100]
	}
	return h
}

func (a app) keys(ctx context.Context, args []string) int {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "status":
		return a.keysStatus(ctx)
	case "enrol", "enroll", "setup":
		return a.keysEnrol(ctx)
	case "recover":
		return a.keysRecover(ctx)
	case "approve":
		return a.keysApprove(ctx, args)
	case "remove", "rm":
		return a.keysRemove(ctx, args)
	case "members":
		return a.keysMembers(ctx, args)
	case "trust":
		return a.keysTrust(ctx, args)
	case "sync":
		return a.keysSync(ctx, false)
	case "restart":
		return a.keysRestart(ctx, args)
	case "help", "-h", "--help":
		return a.keysUsage(0)
	default:
		fmt.Fprintf(a.stderr, "unknown keys command %q\n", sub)
		return a.keysUsage(2)
	}
}

func (a app) keysUsage(code int) int {
	w := a.stderr
	if code == 0 {
		w = a.stdout
	}
	fmt.Fprintln(w, "Secure ShareNet keys (end-to-end encrypted sharenets):")
	fmt.Fprintln(w, "  s2u keys                      this account's key, this device, and your devices")
	fmt.Fprintln(w, "  s2u keys enrol                create the account key here and show the recovery code once")
	fmt.Fprintln(w, "  s2u keys recover              give this device the key with your recovery code")
	fmt.Fprintln(w, "  s2u keys approve <device-id>  give another of your devices the key (compare the id first)")
	fmt.Fprintln(w, "  s2u keys remove <device-id>   stop giving a device new keys")
	fmt.Fprintln(w, "  s2u keys members <sharenet>   members' key fingerprints and whether this device trusts them")
	fmt.Fprintln(w, "  s2u keys trust <account-id> <fingerprint>  accept a member's new key after comparing it")
	fmt.Fprintln(w, "  s2u keys sync                 hand the key to waiting members and rotate where due")
	fmt.Fprintln(w, "  s2u keys restart <sharenet>   start a fresh key when nobody left holds the old one (owner/manager)")
	return code
}

func (a app) keysStatus(ctx context.Context) int {
	sn, _, err := a.secureSession(ctx)
	if err != nil {
		return a.fail("keys", err)
	}
	st, err := sn.Keys.Client.KeysStatus(ctx)
	if err != nil {
		return a.fail("read keys", err)
	}
	fmt.Fprintf(a.stdout, "This device: %s\n", sn.Keys.DeviceID())
	if !st.Enrolled {
		fmt.Fprintln(a.stdout, "No Secure ShareNet key on this account yet. Run `s2u keys enrol` to create one.")
		return 0
	}
	fmt.Fprintf(a.stdout, "Account key fingerprint: %s\n", clicore.KeyFingerprint(sn.Keys.AccountID, st.PublicKey))
	here := "not approved yet: approve it from an enrolled device (`s2u keys approve " + sn.Keys.DeviceID() + "`) or run `s2u keys recover`"
	if _, err := sn.AccountKey(ctx); err == nil {
		here = "holds the key"
	} else if !errors.Is(err, clicore.ErrDeviceNotApproved) {
		return a.fail("open the account key", err)
	}
	fmt.Fprintf(a.stdout, "This device %s\n\n", here)
	tw := tabwriter.NewWriter(a.stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "DEVICE ID\tLABEL\tSTATUS")
	for _, d := range st.Devices {
		status := "waiting for approval"
		if d.Approved {
			status = "approved"
		}
		if d.ID == sn.Keys.DeviceID() {
			status += " (this device)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", d.ID, orDash(d.Label), status)
	}
	tw.Flush()
	return 0
}

func (a app) keysEnrol(ctx context.Context) int {
	sn, _, err := a.secureSession(ctx)
	if err != nil {
		return a.fail("keys", err)
	}
	st, err := sn.Keys.Client.KeysStatus(ctx)
	if err != nil {
		return a.fail("read keys", err)
	}
	if st.Enrolled {
		if _, err := sn.AccountKey(ctx); err == nil {
			fmt.Fprintln(a.stdout, "This account already has a key and this device holds it.")
			return 0
		}
		if err := sn.Keys.RegisterDevice(ctx, deviceLabel()); err != nil && !isAPIConflict(err) {
			return a.fail("register this device", err)
		}
		fmt.Fprintf(a.stdout, "This account already has a key. This device (%s) is waiting:\n", sn.Keys.DeviceID())
		fmt.Fprintf(a.stdout, "  on a device that holds it run: s2u keys approve %s\n", sn.Keys.DeviceID())
		fmt.Fprintln(a.stdout, "  or here run: s2u keys recover")
		return 0
	}
	code, err := sn.Enrol(ctx, deviceLabel())
	if err != nil {
		return a.fail("create the account key", err)
	}
	fmt.Fprintln(a.stdout, "Secure ShareNet key created. Your RECOVERY CODE (shown only now):")
	fmt.Fprintln(a.stdout)
	fmt.Fprintf(a.stdout, "    %s\n", code)
	fmt.Fprintln(a.stdout)
	fmt.Fprintln(a.stdout, "Write it down and keep it somewhere safe. If you lose every device AND this code,")
	fmt.Fprintln(a.stdout, "no one (including Share2Us) can open your Secure ShareNet files.")
	return 0
}

func (a app) keysRecover(ctx context.Context) int {
	sn, _, err := a.secureSession(ctx)
	if err != nil {
		return a.fail("keys", err)
	}
	var code string
	if a.stdinIsTTY != nil && !a.stdinIsTTY(a.input()) {
		// Piped (scripts, `echo CODE | s2u keys recover`): one line from stdin.
		line, err := bufio.NewReader(a.input()).ReadString('\n')
		if err != nil && line == "" {
			return a.fail("read recovery code", err)
		}
		code = strings.TrimSpace(line)
	} else {
		reader := a.readPassword
		if reader == nil {
			reader = readPasswordNoEcho
		}
		if code, err = reader("Recovery code: ", a.stderr); err != nil {
			return a.fail("read recovery code", err)
		}
	}
	if err := sn.Recover(ctx, code, deviceLabel()); err != nil {
		return a.fail("recover", err)
	}
	fmt.Fprintln(a.stdout, "This device now holds your Secure ShareNet key.")
	return 0
}

func (a app) keysApprove(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(a.stderr, "usage: s2u keys approve <device-id>   (the id the new device shows in `s2u keys`)")
		return 2
	}
	sn, _, err := a.secureSession(ctx)
	if err != nil {
		return a.fail("keys", err)
	}
	if err := sn.ApproveDevice(ctx, strings.TrimSpace(args[0])); err != nil {
		return a.fail("approve device", err)
	}
	fmt.Fprintf(a.stdout, "Approved %s: it can now open your Secure ShareNets.\n", args[0])
	return 0
}

func (a app) keysRemove(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(a.stderr, "usage: s2u keys remove <device-id>")
		return 2
	}
	sn, _, err := a.secureSession(ctx)
	if err != nil {
		return a.fail("keys", err)
	}
	if err := sn.Keys.Client.RemoveKeyDevice(ctx, strings.TrimSpace(args[0])); err != nil {
		return a.fail("remove device", err)
	}
	fmt.Fprintln(a.stdout, "Removed. It gets no new keys; it keeps what it already had. If the device was lost or")
	fmt.Fprintln(a.stdout, "stolen, reset your keys in the portal and then rotate your Secure ShareNets.")
	return 0
}

func (a app) keysMembers(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(a.stderr, "usage: s2u keys members <sharenet-id>")
		return 2
	}
	sn, _, err := a.secureSession(ctx)
	if err != nil {
		return a.fail("keys", err)
	}
	st, err := sn.Keys.Client.SecureState(ctx, args[0])
	if err != nil {
		return a.fail("read the sharenet", err)
	}
	if !st.Secure {
		fmt.Fprintln(a.stdout, "This is not a Secure ShareNet.")
		return 0
	}
	fmt.Fprintf(a.stdout, "Key epoch %d", st.CurrentEpoch)
	if st.NeedsRotation {
		fmt.Fprint(a.stdout, " (a member left: the next post or `s2u keys sync` rotates it)")
	}
	fmt.Fprintln(a.stdout)
	pending := map[string]bool{}
	for _, m := range st.Pending {
		pending[m.AccountID] = true
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ACCOUNT\tFINGERPRINT\tTHIS DEVICE")
	for _, m := range st.Members {
		if len(m.PublicKey) == 0 {
			fmt.Fprintf(tw, "%s\t-\tnot enrolled yet\n", m.AccountID)
			continue
		}
		trust := "trusted"
		fp := clicore.KeyFingerprint(m.AccountID, m.PublicKey)
		if ok, err := sn.Pins.IsTrusted(m.AccountID, m.PublicKey); err != nil {
			return a.fail("read pins", err)
		} else if !ok {
			if known, _ := sn.Pins.Known(m.AccountID); known {
				trust = "KEY CHANGED: compare, then `s2u keys trust " + m.AccountID + " " + strings.ReplaceAll(fp, " ", "") + "`"
			} else {
				trust = "not seen before (trusted on first use when you post or sync)"
			}
		}
		if m.AccountID == sn.Keys.AccountID {
			trust = "you (" + trust + ")"
		}
		if pending[m.AccountID] {
			trust += ", waiting for the key"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", m.AccountID, fp, trust)
	}
	tw.Flush()
	return 0
}

func (a app) keysTrust(ctx context.Context, args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(a.stderr, "usage: s2u keys trust <account-id> <fingerprint>   (compare the fingerprint with the member first;")
		fmt.Fprintln(a.stderr, "       `s2u keys members <sharenet>` shows it)")
		return 2
	}
	typedFP := strings.Join(args[1:], "")
	sn, _, err := a.secureSession(ctx)
	if err != nil {
		return a.fail("keys", err)
	}
	account := strings.TrimSpace(args[0])
	nets, err := sn.Keys.Client.ListSharenets(ctx)
	if err != nil {
		return a.fail("list sharenets", err)
	}
	for _, n := range nets {
		if !n.Secure {
			continue
		}
		st, err := sn.Keys.Client.SecureState(ctx, n.ID)
		if err != nil {
			continue
		}
		for _, m := range st.Members {
			if m.AccountID == account && len(m.PublicKey) > 0 {
				if !clicore.SameFingerprint(typedFP, clicore.KeyFingerprint(account, m.PublicKey)) {
					fmt.Fprintln(a.stderr, "that fingerprint doesn't match the key the server now offers for this member; nothing was trusted")
					return 1
				}
				if err := sn.Pins.Trust(account, m.PublicKey); err != nil {
					return a.fail("trust", err)
				}
				fmt.Fprintf(a.stdout, "Trusted %s with fingerprint %s.\n", account, clicore.KeyFingerprint(account, m.PublicKey))
				return 0
			}
		}
	}
	fmt.Fprintln(a.stderr, "no member with that account id and an enrolled key in your Secure ShareNets")
	return 1
}

// keysSync hands the key to waiting members and rotates where a member left, across every
// Secure ShareNet this account is in. quiet is for the daemon.
func (a app) keysSync(ctx context.Context, quiet bool) int {
	sn, _, err := a.secureSession(ctx)
	if err != nil {
		if quiet {
			return 1
		}
		return a.fail("keys", err)
	}
	return a.syncSecureSharenets(ctx, sn, quiet)
}

func (a app) syncSecureSharenets(ctx context.Context, sn *clicore.SecureNet, quiet bool) int {
	out := a.stdout
	if quiet {
		out = io.Discard
	}
	if _, err := sn.AccountKey(ctx); err != nil {
		if !quiet {
			return a.fail("open the account key", err)
		}
		return 1
	}
	nets, err := sn.Keys.Client.ListSharenets(ctx)
	if err != nil {
		if !quiet {
			return a.fail("list sharenets", err)
		}
		return 1
	}
	problems := 0
	for _, n := range nets {
		if !n.Secure {
			continue
		}
		_ = sn.Pins.MarkSecure(n.ID)
		st, err := sn.Keys.Client.SecureState(ctx, n.ID)
		if err != nil {
			problems++
			fmt.Fprintf(a.stderr, "%s: %v\n", n.Name, err)
			continue
		}
		if st.NeedsRotation && n.Role != "viewer" {
			if epoch, err := sn.Rotate(ctx, n.ID, false); err == nil {
				fmt.Fprintf(out, "%s: rotated to key epoch %d\n", n.Name, epoch)
			} else if !errors.Is(err, clicore.ErrNoEpochKeyHere) {
				problems++
				fmt.Fprintf(a.stderr, "%s: rotate: %v\n", n.Name, err)
			}
		}
		added, skipped, err := sn.HandOver(ctx, n.ID)
		if err != nil {
			problems++
			fmt.Fprintf(a.stderr, "%s: hand over: %v\n", n.Name, err)
			continue
		}
		for _, s := range skipped {
			problems++
			fmt.Fprintf(a.stderr, "%s: %v\n", n.Name, s)
		}
		if added > 0 {
			fmt.Fprintf(out, "%s: handed the key to %d member(s)\n", n.Name, added)
		}
	}
	if problems > 0 {
		return 1
	}
	fmt.Fprintln(out, "Secure ShareNets are up to date.")
	return 0
}

func (a app) keysRestart(ctx context.Context, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(a.stderr, "usage: s2u keys restart <sharenet-id> --yes")
		return 2
	}
	confirmed := len(args) > 1 && args[1] == "--yes"
	if !confirmed {
		fmt.Fprintln(a.stderr, "This starts a fresh key with no link to the old one: files posted before it can't be")
		fmt.Fprintln(a.stderr, "opened through it. It is only allowed when no remaining member holds the current key.")
		fmt.Fprintf(a.stderr, "Run again with --yes: s2u keys restart %s --yes\n", args[0])
		return 2
	}
	sn, _, err := a.secureSession(ctx)
	if err != nil {
		return a.fail("keys", err)
	}
	epoch, err := sn.Rotate(ctx, args[0], true)
	if err != nil {
		return a.fail("restart the sharenet key", err)
	}
	fmt.Fprintf(a.stdout, "Started key epoch %d.\n", epoch)
	return 0
}

func isAPIConflict(err error) bool {
	var apiErr *clicore.APIError
	return errors.As(err, &apiErr) && apiErr.Status == 409
}

// ---- sharenet files ----

func (a app) sharenet(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return a.sharenetUsage(2)
	}
	switch args[0] {
	case "list":
		return a.sharenetList(ctx)
	case "create":
		return a.sharenetCreate(ctx, args[1:])
	case "files", "ls":
		return a.sharenetFiles(ctx, args[1:])
	case "get", "download":
		return a.sharenetGet(ctx, args[1:])
	case "help", "-h", "--help":
		return a.sharenetUsage(0)
	default:
		// `s2u sharenet <name|id> [list|agents]`: that sharenet's projects and agents.
		if len(args) == 1 || (len(args) == 2 && (args[1] == "list" || args[1] == "agents")) {
			return a.sharenetShow(ctx, args[0])
		}
		// `s2u sharenet <name>[.<project>] <command> ...`: management.
		switch args[1] {
		case "members", "invite", "remove", "project", "projects", "code", "requests", "approve", "reject", "activity", "say":
			return a.sharenetManage(ctx, args[0], args[1], args[2:])
		}
		fmt.Fprintf(a.stderr, "unknown sharenet command %q\n", args[0])
		return a.sharenetUsage(2)
	}
}

// resolveSharenet finds one of the caller's sharenets by name (case-insensitive) or id.
func (a app) resolveSharenet(ctx context.Context, client *clicore.Client, q string) (clicore.SharenetSummary, error) {
	nets, err := client.ListSharenets(ctx)
	if err != nil {
		return clicore.SharenetSummary{}, err
	}
	for _, n := range nets {
		if n.ID == q || strings.EqualFold(n.Name, q) {
			return n, nil
		}
	}
	return clicore.SharenetSummary{}, fmt.Errorf("no sharenet %q (see `%s sharenet list`)", q, commandName)
}

func (a app) sharenetList(ctx context.Context) int {
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	nets, err := client.ListSharenets(ctx)
	if err != nil {
		return a.fail("sharenet list", err)
	}
	if len(nets) == 0 {
		fmt.Fprintf(a.stdout, "You're in no sharenets. Create or join one in the portal: %s\n", portalSharenetsURL())
		return 0
	}
	for _, n := range nets {
		kind := ""
		if n.Secure {
			kind = "  Secure (E2E)"
		}
		projects := "?"
		if ps, err := client.ListSharenetProjects(ctx, n.ID); err == nil {
			names := make([]string, 0, len(ps))
			for _, p := range ps {
				names = append(names, p.Name)
			}
			projects = fmt.Sprintf("%d (%s)", len(ps), strings.Join(names, ", "))
		}
		fmt.Fprintf(a.stdout, "%-20s %-7s projects %s%s\n    id %s\n", n.Name, n.Role, projects, kind, n.ID)
	}
	fmt.Fprintf(a.stdout, "\nAgents in one: %s sharenet <name>. Send: %s agent send --to <sharenet>.<agent> --prompt \"...\"\n", commandName, commandName)
	return 0
}

// sharenetShow lists a sharenet's projects and their member agents, with the address
// `agent send --to` takes. Agents are visible only in projects where this device runs a
// member agent (the server's rule).
func (a app) sharenetShow(ctx context.Context, q string) int {
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	n, err := a.resolveSharenet(ctx, client, q)
	if err != nil {
		fmt.Fprintln(a.stderr, cliErrorMessage(err))
		return 1
	}
	projects, err := client.ListSharenetProjects(ctx, n.ID)
	if err != nil {
		return a.fail("sharenet projects", err)
	}
	fmt.Fprintf(a.stdout, "%s  (%s, %d project(s))\n", n.Name, n.Role, len(projects))
	config, _ := clicore.LoadConfig()
	for _, p := range projects {
		fmt.Fprintf(a.stdout, "\n  project %s\n", p.Name)
		members, err := client.ListProjectAgents(ctx, p.ID)
		if err != nil {
			fmt.Fprintln(a.stdout, "    (agents hidden: no agent on this device is a member of this project)")
			continue
		}
		if len(members) == 0 {
			fmt.Fprintln(a.stdout, "    no agents online")
			continue
		}
		for _, m := range onePerAgent(members) {
			name := config.AgentDisplayName(m.AgentID, m.Function)
			if name == "" {
				name = m.AgentID
			}
			owner := ""
			if m.Yours {
				owner = "  (yours)"
			}
			fmt.Fprintf(a.stdout, "    %-32s %-8s %-10s %s%s\n", n.Name+"."+name, m.Tool, m.Status, m.AgentID, owner)
		}
	}
	return 0
}

func (a app) sharenetUsage(code int) int {
	w := a.stderr
	if code == 0 {
		w = a.stdout
	}
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  s2u sharenet list                                    your sharenets and their projects")
	fmt.Fprintln(w, "  s2u sharenet <name|id> [list]                        its projects and agents (send to them as <sharenet>.<agent>)")
	fmt.Fprintln(w, "  s2u sharenet create <name> [--secure] [--project NAME] [--add-my-agents] [--can-post]")
	fmt.Fprintln(w, "  s2u sharenet <name> members | invite <email>... [--role R] [--bring-agents PROJECT] | remove <email>")
	fmt.Fprintln(w, "  s2u sharenet <name> project create <project>")
	fmt.Fprintln(w, "  s2u sharenet <name>[.<project>] code [--uses N] [--expires 7d]   join code for an agent's session")
	fmt.Fprintln(w, "  s2u sharenet <name>[.<project>] requests | approve <id> | reject <id>")
	fmt.Fprintln(w, "  s2u sharenet <name>[.<project>] activity [--since ID] [--limit N] [--json]   what happened in a channel")
	fmt.Fprintln(w, "  s2u sharenet <name>[.<project>] say <text> [--reply ID] [--as-me]   post a message (as this agent inside its session)")
	fmt.Fprintln(w, "  s2u sharenet <name>.<project> dm @<user> <text>       message a member privately, as this agent (they reply to you as tasks)")
	fmt.Fprintln(w, "  s2u agent add <agent|.> <name>[.<project>] [--can-post]          add your own agent to a project")
	fmt.Fprintln(w, "      (. = this session's agent, or the one bound to this folder or a parent; a folder path works too)")
	fmt.Fprintln(w, "  (membership changes ask on a terminal; add --yes in scripts)")
	fmt.Fprintln(w, "  s2u sharenet files <name|id>                         list the sharenet's files")
	fmt.Fprintln(w, "  s2u sharenet get <name|id> <file-id> [-o path]       download one (decrypting a Secure ShareNet's)")
	fmt.Fprintln(w, "Post with: s2u <file> --sharenet <id> (or --project <id>). Deleting a sharenet and handing over ownership are in the portal.")
	return code
}

func (a app) sharenetFiles(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(a.stderr, "usage: s2u sharenet files <sharenet-id>")
		return 2
	}
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	netID := args[0]
	if n, rerr := a.resolveSharenet(ctx, client, args[0]); rerr == nil {
		netID = n.ID
	}
	files, err := client.ListSharenetFiles(ctx, netID)
	if err != nil {
		return a.fail("list files", err)
	}
	if len(files) == 0 {
		fmt.Fprintln(a.stdout, "No files yet.")
		return 0
	}
	var sn *clicore.SecureNet
	tw := tabwriter.NewWriter(a.stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "FILE ID\tNAME\tSIZE\tPOSTED BY")
	for _, f := range files {
		name := f.Name
		if f.SecureEpoch > 0 {
			if sn == nil {
				if sn, _, err = a.secureSession(ctx); err != nil {
					return a.fail("keys", err)
				}
			}
			if _, real, err := sn.OpenFile(ctx, args[0], f.SecureEpoch, f.SecureFileID, f.FileKeyWrap, f.FileNameCT); err == nil {
				name = displayName(real)
			} else {
				name = "(encrypted: can't open on this device)"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", f.ID, displayName(name), f.SizeBytes, orDash(f.PostedByName))
	}
	tw.Flush()
	return 0
}

func (a app) sharenetGet(ctx context.Context, args []string) int {
	var pos []string
	outPath := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-o" || args[i] == "--output":
			if i+1 >= len(args) {
				fmt.Fprintln(a.stderr, "-o needs a path")
				return 2
			}
			i++
			outPath = args[i]
		case strings.HasPrefix(args[i], "--output="):
			outPath = strings.TrimPrefix(args[i], "--output=")
		default:
			pos = append(pos, args[i])
		}
	}
	if len(pos) != 2 {
		fmt.Fprintln(a.stderr, "usage: s2u sharenet get <sharenet-id> <file-id> [-o path]")
		return 2
	}
	netID, fileID := pos[0], pos[1]
	client, _, ok := a.authClient()
	if !ok {
		fmt.Fprintf(a.stderr, "not logged in; run `%s login`\n", commandName)
		return 1
	}
	dl, err := client.SharenetFileDownload(ctx, netID, fileID)
	if err != nil {
		return a.fail("get download link", err)
	}
	name := dl.Name
	var dataKey []byte
	if dl.SecureEpoch > 0 {
		sn, _, err := a.secureSession(ctx)
		if err != nil {
			return a.fail("keys", err)
		}
		if dataKey, name, err = sn.OpenFile(ctx, netID, dl.SecureEpoch, dl.SecureFileID, dl.FileKeyWrap, dl.FileNameCT); err != nil {
			return a.fail("open the file key", err)
		}
	}
	dest := outPath
	if dest == "" {
		dest = clicore.UniquePath(safeLocalName(name))
	}
	tmp, err := os.CreateTemp(filepath.Dir(absOrDot(dest)), ".s2u-get-*")
	if err != nil {
		return a.fail("create temp file", err)
	}
	defer os.Remove(tmp.Name())
	if err := client.DownloadURL(ctx, dl.URL, tmp); err != nil {
		tmp.Close()
		return a.fail("download", err)
	}
	if dataKey != nil {
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			tmp.Close()
			return a.fail("read download", err)
		}
		out, err := os.CreateTemp(filepath.Dir(absOrDot(dest)), ".s2u-dec-*")
		if err != nil {
			tmp.Close()
			return a.fail("create temp file", err)
		}
		defer os.Remove(out.Name())
		if err := clicore.DecryptStream(out, tmp, dataKey); err != nil {
			out.Close()
			tmp.Close()
			return a.fail("decrypt", err)
		}
		tmp.Close()
		if err := out.Close(); err != nil {
			return a.fail("write file", err)
		}
		if err := os.Rename(out.Name(), dest); err != nil {
			return a.fail("save file", err)
		}
	} else {
		if err := tmp.Close(); err != nil {
			return a.fail("write file", err)
		}
		if err := os.Rename(tmp.Name(), dest); err != nil {
			return a.fail("save file", err)
		}
	}
	fmt.Fprintf(a.stdout, "Saved %s\n", dest)
	return 0
}

func absOrDot(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return "."
}

// safeLocalName keeps only a file name (a sharenet file's name comes from another member,
// or from a decrypted field, and must not pick a directory).
func safeLocalName(name string) string {
	name = displayName(name)
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`:*?"<>|`, r) {
			return '_' // Windows: alternate data streams and invalid characters
		}
		return r
	}, name)
	name = strings.TrimRight(name, ". ")
	if name == "" || name == "." || name == ".." || name == "/" {
		return "sharenet-file"
	}
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		name = "_" + name
	}
	return name
}

// displayName drops control characters (terminal escapes) from a name another member
// chose, before it is printed or used as a file name.
func displayName(name string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, name)
}

var errSecureNote = errors.New("a note can't be posted to a Secure ShareNet")

// prepareSecurePost decides whether a sharenet/project post goes to a Secure ShareNet and,
// if so, seals its key and name. ok=false means post as usual. It fails closed: an error
// reading the sharenet, or a server claiming a sharenet this device knows is Secure isn't,
// stops the post rather than sending anything in the clear.
func (a app) prepareSecurePost(ctx context.Context, client *clicore.Client, sharenetID, projectID, fileName string, hasNote bool) (clicore.SealedPost, string, bool, error) {
	pinPath, err := clicore.PinStorePath()
	if err != nil {
		return clicore.SealedPost{}, "", false, err
	}
	pins, err := clicore.OpenPinStore(pinPath)
	if err != nil {
		return clicore.SealedPost{}, "", false, err
	}
	netID, secure := sharenetID, false
	if netID == "" {
		n, found, err := client.SharenetForProject(ctx, projectID, false)
		if err != nil {
			return clicore.SealedPost{}, "", false, fmt.Errorf("look up the project's sharenet: %w", err)
		}
		if !found {
			return clicore.SealedPost{}, "", false, nil // the post itself reports an unknown project
		}
		netID, secure = n.ID, n.Secure
	} else {
		st, err := client.SecureState(ctx, netID)
		var apiErr *clicore.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			if known, _ := pins.KnownSecure(netID); known {
				return clicore.SealedPost{}, netID, true, errors.New("this device knows the sharenet is a Secure ShareNet but the server says it doesn't exist; nothing was sent")
			}
			return clicore.SealedPost{}, "", false, nil // not a member: the post reports it
		}
		if err != nil {
			return clicore.SealedPost{}, "", false, fmt.Errorf("check whether the sharenet is a Secure ShareNet: %w", err)
		}
		secure = st.Secure
	}
	if known, err := pins.KnownSecure(netID); err != nil {
		return clicore.SealedPost{}, "", false, err
	} else if known && !secure {
		return clicore.SealedPost{}, netID, true, errors.New("the server says this Secure ShareNet is no longer secure, which can't happen; nothing was sent")
	}
	if !secure {
		return clicore.SealedPost{}, "", false, nil
	}
	if err := pins.MarkSecure(netID); err != nil {
		return clicore.SealedPost{}, netID, true, err
	}
	if hasNote {
		return clicore.SealedPost{}, netID, true, errSecureNote
	}
	sn, _, err := a.secureSession(ctx)
	if err != nil {
		return clicore.SealedPost{}, netID, true, err
	}
	p, err := sn.PreparePost(ctx, netID, fileName)
	return p, netID, true, err
}

// printSecurePostAPIError explains the Secure ShareNet refusals a post can race into.
func (a app) printSecurePostAPIError(err error) bool {
	var apiErr *clicore.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case "secure_required":
		fmt.Fprintln(a.stderr, "this sharenet just became a Secure ShareNet; run the command again to post it encrypted")
	case "wrong_epoch", "rotation_required":
		fmt.Fprintln(a.stderr, "the sharenet's key changed while posting (a member joined or left); run the command again")
	case "no_epoch":
		fmt.Fprintln(a.stderr, "this Secure ShareNet has no key yet; run the command again to create it")
	default:
		return false
	}
	return true
}

// daemonSyncSecureKeys is the daemon's periodic Secure ShareNet job. Not enrolled or not
// approved on this device is normal (nothing to do), not an error.
func (a app) daemonSyncSecureKeys(ctx context.Context) error {
	sn, _, err := a.secureSessionMode(ctx, true)
	if err != nil {
		return nil
	}
	if _, err := sn.AccountKey(ctx); err != nil {
		var unknown *clicore.KeyUnknownError
		if errors.Is(err, clicore.ErrKeysNotEnrolled) || errors.Is(err, clicore.ErrDeviceNotApproved) || errors.As(err, &unknown) {
			return nil
		}
		return err
	}
	if a.syncSecureSharenets(ctx, sn, true) != 0 {
		return errors.New("some Secure ShareNets could not be synced (details above)")
	}
	return nil
}

// onePerAgent collapses the directory's per-session rows into one line per agent. The
// API lists every live session (that is what a send routes to), so an agent with two
// sessions (a resumed one, a stale one from before a restart) printed twice with two
// statuses. The kept row has the most telling presence: busy over available over the
// rest, so the listing never shows a busy agent as free.
func onePerAgent(rows []clicore.ProjectAgentAddress) []clicore.ProjectAgentAddress {
	rank := func(status string) int {
		switch status {
		case "busy":
			return 0
		case "available":
			return 1
		default:
			return 2
		}
	}
	out := make([]clicore.ProjectAgentAddress, 0, len(rows))
	at := map[string]int{}
	for _, r := range rows {
		key := r.AgentID
		if key == "" {
			out = append(out, r)
			continue
		}
		if i, seen := at[key]; seen {
			if rank(r.Status) < rank(out[i].Status) {
				out[i] = r
			}
			continue
		}
		at[key] = len(out)
		out = append(out, r)
	}
	return out
}
