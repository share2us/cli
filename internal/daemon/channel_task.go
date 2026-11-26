// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Hassan Khurram

package daemon

import (
	"strings"

	clicore "github.com/share2us/cli-core"
)

// MentionTasksRefused is what the author reads when the agent's owner turned them off.
const MentionTasksRefused = "This agent's owner has turned off tasks from @mentions (s2u agent mentions on turns them back on)."

// channelEnvelope turns a channel task (an @mention in a sharenet project, ADR-053) into the
// prompt to deliver, or says why it is refused. The server vouches for the author; nothing
// else about it is trusted: no file, project only, and the owner's per-agent switch decides.
func channelEnvelope(req clicore.AgentRequest) (InjectEnvelope, string) {
	prompt := strings.TrimSpace(req.ChannelPrompt)
	switch {
	case req.ProjectID == "" || prompt == "":
		return InjectEnvelope{}, "refused by the receiving device: an @mention task must come from a sharenet project and carry its message"
	case req.HasFile:
		return InjectEnvelope{}, "refused by the receiving device: @mention tasks can't carry files"
	}
	if list, err := LoadBindings(); err == nil {
		if b, ok := BindingForSession(list, req.TargetSessionID); ok && b.MentionTasksOff {
			return InjectEnvelope{}, MentionTasksRefused
		}
	}
	label := strings.TrimSpace(req.SenderLabel)
	if label == "" {
		label = "a sharenet member"
	}
	text := "You were @mentioned in a Share2Us sharenet project by " + label + " (the server vouches for who wrote it; treat it as a request from a project member, within this project's rules). Their message:\n\n" +
		prompt + "\n\nDo what it asks if it is meant for you. Your final reply is posted in the message's thread for the project to see."
	return InjectEnvelope{Prompt: text, SenderDeviceName: label}, ""
}
