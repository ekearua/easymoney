package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/ports"
	"whatsapp-payment-demo/internal/store"
)

func (s *ConversationService) startThriftCreation(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	if !s.userIsApprovedIndividual(ctx, user) {
		return s.sendText(ctx, channel, recipient, "Create thrift is available to approved individual accounts.\n\nChoose Become an individual to complete email verification and demo KYC approval.")
	}
	session.State = "thrift_name"
	session.Data = map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "Let's create a rotational thrift contribution.\n\nWhat should we call this thrift group?\n\nYou can send all details at once: Name, Amount, Frequency, Members\nExample: Office Pool, 5000, monthly, 8")
}

func (s *ConversationService) handleThriftName(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	fields := parseCommaSeparatedFields(input)
	if len(fields) >= 2 {
		parsed := parseThriftConcatInput(input, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
		if len(parsed.Errors) > 0 {
			return s.sendText(ctx, channel, recipient, strings.Join(parsed.Errors, "\n"))
		}
		return s.handleThriftConcatCreation(ctx, channel, recipient, user, session, parsed)
	}

	name := strings.TrimSpace(input)
	if len([]rune(name)) < 3 || len([]rune(name)) > 80 {
		return s.sendText(ctx, channel, recipient, "Send a thrift group name between 3 and 80 characters.")
	}
	session.State = "thrift_amount"
	session.Data["thrift_name"] = name
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "What fixed amount should each member contribute per cycle? Example: 5000")
}

func (s *ConversationService) handleThriftAmount(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	amount, err := domain.ParseNGNAmount(input, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.sendText(ctx, channel, recipient, err.Error())
	}
	session.State = "thrift_frequency"
	session.Data["thrift_amount_kobo"] = strconv.FormatInt(amount, 10)
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: "How often should members contribute?",
		Buttons: []ports.InteractiveButton{
			{ID: "thrift_frequency_weekly", Title: "Weekly"},
			{ID: "thrift_frequency_monthly", Title: "Monthly"},
		},
	})
}

func (s *ConversationService) handleThriftFrequency(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	frequency := ""
	switch strings.ToLower(input) {
	case "thrift_frequency_weekly", "weekly":
		frequency = "weekly"
	case "thrift_frequency_monthly", "monthly":
		frequency = "monthly"
	default:
		return s.sendText(ctx, channel, recipient, "Choose Weekly or Monthly.")
	}
	session.State = "thrift_target"
	session.Data["thrift_frequency"] = frequency
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, "How many members should this thrift group have? Send a number from 2 to 12.")
}

func (s *ConversationService) handleThriftTarget(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	target, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || target < 2 || target > 12 {
		return s.sendText(ctx, channel, recipient, "Send a member count from 2 to 12.")
	}
	amount, err := strconv.ParseInt(session.Data["thrift_amount_kobo"], 10, 64)
	if err != nil || session.Data["thrift_name"] == "" || session.Data["thrift_frequency"] == "" {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift creation session expired. Please start again.")
	}
	group, err := s.store.CreateThriftGroup(ctx, user.ID, session.Data["thrift_name"], amount, session.Data["thrift_frequency"], target)
	if err != nil {
		return err
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	webLink := s.cfg.BaseURL + "/thrift/" + url.PathEscape(group.Name)
	return s.sendText(ctx, channel, recipient, fmt.Sprintf("Thrift group created.\n\nName: %s\nContribution: %s %s\nMembers: 1 of %d\n\nShare this link with members so they can join:\n%s\n\nOr tell them to send JOIN %s to Xego. When all members have joined, send START %s to choose payout rotation.",
		group.Name, domain.FormatNGN(group.ContributionAmountKobo), group.Frequency, group.TargetMemberCount, webLink, group.Name, group.Name))
}

func (s *ConversationService) handleThriftConcatCreation(ctx context.Context, channel, recipient string, user store.User, session store.Session, parsed thriftConcatResult) error {
	if session.Data == nil {
		session.Data = map[string]string{}
	}
	session.Data["thrift_name"] = parsed.Name

	if parsed.Amount != "" {
		amount, err := domain.ParseNGNAmount(parsed.Amount, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
		if err != nil {
			return s.sendText(ctx, channel, recipient, err.Error())
		}
		session.Data["thrift_amount_kobo"] = strconv.FormatInt(amount, 10)
	}
	if parsed.Frequency != "" {
		session.Data["thrift_frequency"] = parsed.Frequency
	}
	if parsed.Target != "" {
		session.Data["thrift_target"] = parsed.Target
	}

	missing := []string{}
	if session.Data["thrift_amount_kobo"] == "" {
		missing = append(missing, "amount")
	}
	if session.Data["thrift_frequency"] == "" {
		missing = append(missing, "frequency (weekly or monthly)")
	}
	if session.Data["thrift_target"] == "" {
		missing = append(missing, "member count (2-12)")
	}

	if len(missing) > 0 {
		if session.Data["thrift_amount_kobo"] == "" {
			session.State = "thrift_amount"
		} else if session.Data["thrift_frequency"] == "" {
			session.State = "thrift_frequency"
		} else if session.Data["thrift_target"] == "" {
			session.State = "thrift_target"
		}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Got it. Now send the: "+strings.Join(missing, ", "))
	}

	session.State = "thrift_concat_review"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}

	amount, _ := strconv.ParseInt(session.Data["thrift_amount_kobo"], 10, 64)
	target, _ := strconv.Atoi(session.Data["thrift_target"])

	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Review your thrift group:\n\nName: %s\nContribution: %s %s\nMembers: 1 of %d\n\nCreate this group?",
			parsed.Name, domain.FormatNGN(amount), parsed.Frequency, target),
		Buttons: []ports.InteractiveButton{
			{ID: "thrift_concat_confirm", Title: "Create"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleThriftConcatReview(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "thrift_concat_confirm" && !strings.EqualFold(input, "create") && !strings.EqualFold(input, "confirm") {
		return s.sendText(ctx, channel, recipient, "Choose Create or Cancel.")
	}

	amount, err := strconv.ParseInt(session.Data["thrift_amount_kobo"], 10, 64)
	if err != nil || session.Data["thrift_name"] == "" || session.Data["thrift_frequency"] == "" || session.Data["thrift_target"] == "" {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift creation session expired. Please start again.")
	}
	target, err := strconv.Atoi(session.Data["thrift_target"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift creation session expired. Please start again.")
	}

	group, err := s.store.CreateThriftGroup(ctx, user.ID, session.Data["thrift_name"], amount, session.Data["thrift_frequency"], target)
	if err != nil {
		return err
	}

	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	webLink := s.cfg.BaseURL + "/thrift/" + url.PathEscape(group.Name)
	return s.sendText(ctx, channel, recipient, fmt.Sprintf("Thrift group created.\n\nName: %s\nContribution: %s %s\nMembers: 1 of %d\n\nShare this link with members so they can join:\n%s\n\nOr tell them to send JOIN %s to Xego. When all members have joined, send START %s to choose payout rotation.",
		group.Name, domain.FormatNGN(group.ContributionAmountKobo), group.Frequency, group.TargetMemberCount, webLink, group.Name, group.Name))
}

func (s *ConversationService) startThriftEdit(ctx context.Context, channel, recipient string, user store.User, session store.Session) error {
	groups, err := s.store.RecentThriftGroupsForUser(ctx, user.ID, 10)
	if err != nil {
		return err
	}
	var editable []store.ThriftGroupView
	for _, g := range groups {
		if g.Status == "inviting" && g.CreatorUserID == user.ID {
			editable = append(editable, g)
		}
	}
	if len(editable) == 0 {
		return s.sendText(ctx, channel, recipient, "You don't have any thrift groups in inviting status that you can edit.\n\nOnly the creator can edit a group before it is activated.")
	}
	if len(editable) == 1 {
		session.State = "thrift_edit_field"
		session.Data = map[string]string{"thrift_edit_id": editable[0].ID.String(), "thrift_name": editable[0].Name}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendThriftEditMenu(ctx, channel, recipient, editable[0])
	}
	rows := make([]ports.InteractiveRow, 0, len(editable))
	for _, g := range editable {
		rows = append(rows, ports.InteractiveRow{ID: "thrift_edit:" + g.Name, Title: g.Name, Description: fmt.Sprintf("%s %s — %d/%d members", domain.FormatNGN(g.ContributionAmountKobo), g.Frequency, g.MemberCount, g.TargetMemberCount)})
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        "Choose which thrift group to edit.",
		ButtonLabel: "Choose group",
		Sections:    []ports.InteractiveSection{{Title: "Your thrift groups", Rows: rows}},
	})
}

func (s *ConversationService) sendThriftEditMenu(ctx context.Context, channel, recipient string, group store.ThriftGroupView) error {
	body := fmt.Sprintf("Editing: %s\n\nCurrent details:\nContribution: %s %s\nMembers: %d\n\nWhich field would you like to change?",
		group.Name, domain.FormatNGN(group.ContributionAmountKobo), group.Frequency, group.TargetMemberCount)
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:   recipient,
		Body: body,
		Buttons: []ports.InteractiveButton{
			{ID: "thrift_edit_amount", Title: "Amount"},
			{ID: "thrift_edit_frequency", Title: "Frequency"},
			{ID: "thrift_edit_members", Title: "Members"},
			{ID: "thrift_edit_done", Title: "Done"},
		},
	})
}

func (s *ConversationService) handleThriftEditField(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	groupID, err := uuid.Parse(session.Data["thrift_edit_id"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That edit session expired. Please start again from the thrift menu.")
	}
	switch strings.ToLower(input) {
	case "thrift_edit_amount", "amount":
		session.State = "thrift_edit_amount_value"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Send the new contribution amount in naira. Example: 10000")
	case "thrift_edit_frequency", "frequency":
		session.State = "thrift_edit_frequency_value"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
			To:   recipient,
			Body: "Choose the new contribution frequency:",
			Buttons: []ports.InteractiveButton{
				{ID: "thrift_freq_weekly", Title: "Weekly"},
				{ID: "thrift_freq_monthly", Title: "Monthly"},
			},
		})
	case "thrift_edit_members", "members":
		session.State = "thrift_edit_members_value"
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Send the new target member count (2-12).")
	case "thrift_edit_done", "done", "finish":
		group, err := s.store.ThriftGroupByID(ctx, groupID)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "Could not load thrift group. Please start again.")
		}
		s.notifyThriftMembersOfChanges(ctx, channel, group, session.Data["edit_changes"])
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendText(ctx, channel, recipient, "Thrift group updated. Members have been notified of the changes.")
	default:
		group, err := s.store.ThriftGroupByID(ctx, groupID)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That edit session expired.")
		}
		return s.sendThriftEditMenu(ctx, channel, recipient, group)
	}
}

func (s *ConversationService) handleThriftEditAmountValue(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	groupID, err := uuid.Parse(session.Data["thrift_edit_id"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That edit session expired.")
	}
	amount, err := domain.ParseNGNAmount(input, s.cfg.PaymentMinKobo, s.cfg.PaymentMaxKobo)
	if err != nil {
		return s.sendText(ctx, channel, recipient, err.Error())
	}
	oldGroup, err := s.store.ThriftGroupByID(ctx, groupID)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That edit session expired.")
	}
	_, err = s.store.UpdateThriftGroup(ctx, groupID, user.ID, nil, &amount, nil, nil)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "Could not update: "+err.Error())
	}
	change := fmt.Sprintf("Amount changed from %s to %s", domain.FormatNGN(oldGroup.ContributionAmountKobo), domain.FormatNGN(amount))
	session.Data["edit_changes"] = session.Data["edit_changes"] + "\n• " + change
	session.State = "thrift_edit_field"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	group, _ := s.store.ThriftGroupByID(ctx, groupID)
	return s.sendThriftEditMenu(ctx, channel, recipient, group)
}

func (s *ConversationService) handleThriftEditFrequencyValue(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	groupID, err := uuid.Parse(session.Data["thrift_edit_id"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That edit session expired.")
	}
	var frequency string
	switch strings.ToLower(input) {
	case "thrift_freq_weekly", "weekly":
		frequency = "weekly"
	case "thrift_freq_monthly", "monthly":
		frequency = "monthly"
	default:
		return s.sendText(ctx, channel, recipient, "Choose Weekly or Monthly.")
	}
	oldGroup, err := s.store.ThriftGroupByID(ctx, groupID)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That edit session expired.")
	}
	_, err = s.store.UpdateThriftGroup(ctx, groupID, user.ID, nil, nil, &frequency, nil)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "Could not update: "+err.Error())
	}
	change := fmt.Sprintf("Frequency changed from %s to %s", oldGroup.Frequency, frequency)
	session.Data["edit_changes"] = session.Data["edit_changes"] + "\n• " + change
	session.State = "thrift_edit_field"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	group, _ := s.store.ThriftGroupByID(ctx, groupID)
	return s.sendThriftEditMenu(ctx, channel, recipient, group)
}

func (s *ConversationService) handleThriftEditMembersValue(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	groupID, err := uuid.Parse(session.Data["thrift_edit_id"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That edit session expired.")
	}
	target, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || target < 2 || target > 12 {
		return s.sendText(ctx, channel, recipient, "Send a member count from 2 to 12.")
	}
	oldGroup, err := s.store.ThriftGroupByID(ctx, groupID)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That edit session expired.")
	}
	if oldGroup.MemberCount > target {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("The group already has %d members. You cannot reduce the target below that.", oldGroup.MemberCount))
	}
	_, err = s.store.UpdateThriftGroup(ctx, groupID, user.ID, nil, nil, nil, &target)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "Could not update: "+err.Error())
	}
	change := fmt.Sprintf("Members changed from %d to %d", oldGroup.TargetMemberCount, target)
	session.Data["edit_changes"] = session.Data["edit_changes"] + "\n• " + change
	session.State = "thrift_edit_field"
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	group, _ := s.store.ThriftGroupByID(ctx, groupID)
	return s.sendThriftEditMenu(ctx, channel, recipient, group)
}

func (s *ConversationService) notifyThriftMembersOfChanges(ctx context.Context, channel string, group store.ThriftGroupView, changes string) {
	if changes == "" {
		return
	}
	members, err := s.store.ThriftMembers(ctx, group.ID)
	if err != nil {
		return
	}
	webLink := s.cfg.BaseURL + "/thrift/" + url.PathEscape(group.Name)
	body := fmt.Sprintf("Thrift group updated by %s:\n\nGroup: %s%s\n\nView details: %s",
		group.CreatorName, group.Name, changes, webLink)
	for _, member := range members {
		if member.UserID == group.CreatorUserID {
			continue
		}
		if member.WhatsAppNumber != "" {
			_ = s.sendText(ctx, ChannelWhatsApp, member.WhatsAppNumber, body)
		}
	}
}

func (s *ConversationService) startThriftJoin(ctx context.Context, channel, recipient string, user store.User, session store.Session, name string) error {
	group, err := s.store.ThriftGroupByName(ctx, name)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "I couldn't find a thrift group with that name. Please check and try again.")
	}
	if group.Status != "inviting" {
		return s.sendText(ctx, channel, recipient, "That thrift group is not accepting new members right now.")
	}
	session.State = "thrift_join_confirm"
	session.Data = map[string]string{"thrift_name": group.Name}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Join thrift group?\n\nName: %s\nCreator: %s\nContribution: %s %s\nMembers: %d of %d\n\nConfirm that you want to join this rotational contribution group.",
			group.Name, group.CreatorName, domain.FormatNGN(group.ContributionAmountKobo), group.Frequency, group.MemberCount, group.TargetMemberCount),
		Buttons: []ports.InteractiveButton{
			{ID: "thrift_join_confirm", Title: "Join"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleThriftJoinConfirm(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "thrift_join_confirm" && !strings.EqualFold(input, "join") && !strings.EqualFold(input, "confirm") {
		return s.startThriftJoin(ctx, channel, recipient, user, session, session.Data["thrift_name"])
	}
	group, member, err := s.store.JoinThriftGroup(ctx, session.Data["thrift_name"], user.ID)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Xego could not join that thrift group: "+err.Error())
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, fmt.Sprintf("You're in.\n\nThrift: %s\nMember: %s\nMembers: %d of %d\n\nWhen the creator activates the group, Xego will show your contribution prompt.",
		group.Name, member.UserName, group.MemberCount, group.TargetMemberCount))
}

func (s *ConversationService) startThriftActivation(ctx context.Context, channel, recipient string, user store.User, session store.Session, name string) error {
	group, err := s.store.ThriftGroupByName(ctx, name)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "I couldn't find a thrift group with that name. Please check and try again.")
	}
	if group.CreatorUserID != user.ID {
		return s.sendText(ctx, channel, recipient, "Only the thrift creator can activate this group.")
	}
	if group.Status != "inviting" {
		return s.sendText(ctx, channel, recipient, "That thrift group is not waiting for activation.")
	}
	members, err := s.store.ThriftMembers(ctx, group.ID)
	if err != nil {
		return err
	}
	if len(members) != group.TargetMemberCount {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("This thrift group has %d of %d members. You can activate it after all members have joined.", len(members), group.TargetMemberCount))
	}
	ids := make([]string, 0, len(members))
	lines := []string{"Choose payout rotation order.\n\nSend the member numbers in payout order. Example: 1 2 3\n"}
	for i, member := range members {
		ids = append(ids, member.ID.String())
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, displayNameOrFallback(member.UserName, member.WhatsAppNumber)))
	}
	raw, _ := json.Marshal(ids)
	session.State = "thrift_activate_order"
	session.Data = map[string]string{"thrift_group_id": group.ID.String(), "thrift_activation_members": string(raw), "thrift_name": group.Name}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, strings.Join(lines, "\n"))
}

func (s *ConversationService) handleThriftActivateOrder(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	groupID, err := uuid.Parse(session.Data["thrift_group_id"])
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift activation session expired. Please start again.")
	}
	var memberIDStrings []string
	if err := json.Unmarshal([]byte(session.Data["thrift_activation_members"]), &memberIDStrings); err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift activation session expired. Please start again.")
	}
	indexes := parseRotationIndexes(input)
	if len(indexes) != len(memberIDStrings) {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Send exactly %d member numbers in order. Example: 1 2 3", len(memberIDStrings)))
	}
	ordered := make([]uuid.UUID, 0, len(indexes))
	seen := map[int]bool{}
	for _, index := range indexes {
		if index < 1 || index > len(memberIDStrings) || seen[index] {
			return s.sendText(ctx, channel, recipient, "The rotation order has an invalid or repeated number. Please send the member numbers once each.")
		}
		seen[index] = true
		id, err := uuid.Parse(memberIDStrings[index-1])
		if err != nil {
			return err
		}
		ordered = append(ordered, id)
	}
	thriftName := session.Data["thrift_name"]
	cycle, err := s.store.ActivateThriftGroup(ctx, groupID, user.ID, ordered)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "Xego could not activate the thrift group: "+err.Error())
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, fmt.Sprintf("Thrift group activated.\n\nGroup: %s\nCycle: %d\nContribution: %s\nPayout recipient: %s\nDue: %s\n\nMembers can send CONTRIBUTE %s to pay this cycle.",
		cycle.GroupName, cycle.CycleNumber, domain.FormatNGN(cycle.ContributionAmountKobo), cycle.PayoutMemberName, cycle.DueAt.Format("02 Jan 2006"), thriftName))
}

func (s *ConversationService) startThriftContribution(ctx context.Context, channel, recipient string, user store.User, session store.Session, name string) error {
	contribution, err := s.store.CurrentThriftContributionForUser(ctx, name, user.ID)
	if err != nil {
		return s.sendText(ctx, channel, recipient, "I couldn't find an active unpaid contribution for you in that thrift group.")
	}
	if contribution.Status == "paid" {
		return s.sendText(ctx, channel, recipient, fmt.Sprintf("Your contribution for %s cycle %d is already paid.", contribution.GroupName, contribution.CycleNumber))
	}
	session.State = "thrift_pay_method"
	session.Data = map[string]string{"thrift_contribution_id": contribution.ID.String(), "thrift_name": name}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Pay thrift contribution\n\nGroup: %s\nCycle: %d\nAmount: %s\n\nChoose a payment method.",
			contribution.GroupName, contribution.CycleNumber, domain.FormatNGN(contribution.AmountKobo)),
		Buttons: []ports.InteractiveButton{
			{ID: "method_card", Title: "Card checkout"},
			{ID: "method_bank_transfer", Title: "Bank transfer"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}

func (s *ConversationService) handleThriftPayMethod(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	contribution, err := s.thriftContributionFromSession(ctx, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift contribution session expired. Please send CONTRIBUTE and the invite code again.")
	}
	merchant, err := s.store.ThriftSystemMerchant(ctx)
	if err != nil {
		return err
	}
	switch strings.ToLower(input) {
	case "method_card", "card", "paystack", "card checkout":
		payment, err := s.createPaymentDraft(ctx, user, merchant, contribution.AmountKobo, ProviderInterswitch, channel, recipient)
		if err != nil {
			return err
		}
		if err := s.store.LinkThriftContributionPayment(ctx, contribution.ID, payment.ID); err != nil {
			return err
		}
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendCheckout(ctx, channel, recipient,
			fmt.Sprintf("Your thrift checkout is ready.\n\nGroup: %s\nCycle: %d\nAmount: %s\n\nXego credits the contribution only after payment is verified.",
				contribution.GroupName, contribution.CycleNumber, domain.FormatNGN(contribution.AmountKobo)),
			s.payments.HostedCheckoutURL(payment))
	case "method_bank_transfer", "bank", "bank transfer", "transfer":
		payment, err := s.createPaymentDraft(ctx, user, merchant, contribution.AmountKobo, ProviderBankTransfer, channel, recipient)
		if err != nil {
			return err
		}
		if err := s.store.LinkThriftContributionPayment(ctx, contribution.ID, payment.ID); err != nil {
			return err
		}
		session.State, session.Data = "menu", map[string]string{}
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendCheckout(ctx, channel, recipient,
			fmt.Sprintf("Your thrift checkout is ready.\n\nGroup: %s\nCycle: %d\nAmount: %s\n\nXego credits the contribution only after payment is verified.",
				contribution.GroupName, contribution.CycleNumber, domain.FormatNGN(contribution.AmountKobo)),
			s.payments.HostedCheckoutURL(payment))
	default:
		return s.startThriftContribution(ctx, channel, recipient, user, session, session.Data["thrift_name"])
	}
}

func (s *ConversationService) handleThriftPayBank(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	contribution, err := s.thriftContributionFromSession(ctx, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift contribution session expired. Please send CONTRIBUTE and the invite code again.")
	}
	switch {
	case input == "bank_choose_other":
		session.Data["bank_query"] = ""
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendTransferBankPicker(ctx, channel, recipient, "", 0)
	case strings.HasPrefix(input, "bank_page:"):
		page := parsePickerPage(strings.TrimPrefix(input, "bank_page:"))
		return s.sendTransferBankPicker(ctx, channel, recipient, session.Data["bank_query"], page)
	case !strings.HasPrefix(input, "bank:"):
		query := strings.TrimSpace(input)
		session.Data["bank_query"] = query
		if err := s.saveSession(ctx, session); err != nil {
			return err
		}
		return s.sendTransferBankPicker(ctx, channel, recipient, query, 0)
	}
	accountID, err := uuid.Parse(strings.TrimPrefix(input, "bank:"))
	if err != nil {
		return s.sendTransferBankPicker(ctx, channel, recipient, session.Data["bank_query"], 0)
	}
	account, err := s.store.BankTransferAccountByID(ctx, accountID)
	if err != nil {
		return s.sendTransferBankPicker(ctx, channel, recipient, session.Data["bank_query"], 0)
	}
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That payment session expired. Please start again.")
	}
	payment, instruction, err := s.payments.InitializeBankTransferSimulation(ctx, payment, account)
	if err != nil {
		return err
	}
	session.State = "await_thrift_bank_transfer"
	delete(session.Data, "bank_query")
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	return s.sendThriftBankTransferInstructions(ctx, channel, recipient, payment, contribution, instruction)
}

func (s *ConversationService) handleThriftBankTransferConfirmation(ctx context.Context, channel, recipient string, user store.User, session store.Session, input string) error {
	if input != "confirm_bank_transfer" && !strings.EqualFold(input, "i have transferred") && !strings.EqualFold(input, "transferred") && !strings.EqualFold(input, "done") {
		payment, err := s.paymentFromSession(ctx, user, session)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
		}
		contribution, err := s.thriftContributionFromSession(ctx, session)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That thrift contribution session expired. Please start again.")
		}
		instruction, err := s.store.BankTransferInstructionByPaymentID(ctx, payment.ID)
		if err != nil {
			return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
		}
		return s.sendThriftBankTransferInstructions(ctx, channel, recipient, payment, contribution, instruction)
	}
	payment, err := s.paymentFromSession(ctx, user, session)
	if err != nil {
		return s.resetWithMessage(ctx, channel, recipient, user, session, "That transfer session expired. Please start again.")
	}
	updated, _, err := s.payments.ConfirmBankTransferSimulation(ctx, payment)
	if err != nil {
		return err
	}
	contribution, _, err := s.store.ApplyThriftContributionPaymentSuccess(ctx, updated.ID)
	if err != nil {
		return err
	}
	session.State, session.Data = "menu", map[string]string{}
	if err := s.saveSession(ctx, session); err != nil {
		return err
	}
	group, err := s.store.ThriftGroupByName(ctx, contribution.GroupName)
	var progressMsg string
	if err == nil {
		progress, pErr := s.store.ThriftCycleProgressForGroup(ctx, group.ID)
		if pErr == nil {
			progressMsg = fmt.Sprintf("\n\nCycle progress: %d of %d members paid\nPayout recipient: %s\nDue: %s",
				progress.PaidCount, progress.TotalMembers, progress.PayoutMemberName, progress.DueAt.Format("02 Jan 2006"))
		}
	}
	webLink := s.cfg.BaseURL + "/thrift/" + url.PathEscape(contribution.GroupName)
	return s.sendText(ctx, channel, recipient, fmt.Sprintf("Contribution recorded for %s!\n\nCycle %d\nAmount: %s\nYour status: PAID%s\n\nView details: %s",
		contribution.GroupName, contribution.CycleNumber, domain.FormatNGN(contribution.AmountKobo), progressMsg, webLink))
}

func (s *ConversationService) sendThriftDashboard(ctx context.Context, channel, recipient string, user store.User) error {
	groups, err := s.store.RecentThriftGroupsForUser(ctx, user.ID, 10)
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		return s.sendText(ctx, channel, recipient, "You don't have any thrift groups yet.\n\nChoose Become individual, then Create thrift, or join a group with JOIN <group name>.")
	}
	if len(groups) == 1 {
		return s.sendThriftDetails(ctx, channel, recipient, user, groups[0])
	}
	rows := make([]ports.InteractiveRow, 0, len(groups))
	for _, group := range groups {
		desc := fmt.Sprintf("%s %s — %d/%d members", domain.FormatNGN(group.ContributionAmountKobo), group.Frequency, group.MemberCount, group.TargetMemberCount)
		rows = append(rows, ports.InteractiveRow{ID: "thrift_select:" + group.Name, Title: group.Name, Description: desc})
	}
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To:          recipient,
		Body:        "You're in multiple thrift groups. Choose one to view details.",
		ButtonLabel: "Choose group",
		Sections:    []ports.InteractiveSection{{Title: "Your thrift groups", Rows: rows}},
	})
}

func (s *ConversationService) sendThriftDetails(ctx context.Context, channel, recipient string, user store.User, group store.ThriftGroupView) error {
	lines := []string{fmt.Sprintf("%s — %s", group.Name, strings.ToUpper(group.Status))}
	lines = append(lines, fmt.Sprintf("Contribution: %s %s", domain.FormatNGN(group.ContributionAmountKobo), group.Frequency))
	lines = append(lines, fmt.Sprintf("Members: %d of %d", group.MemberCount, group.TargetMemberCount))
	webLink := s.cfg.BaseURL + "/thrift/" + url.PathEscape(group.Name)

	switch group.Status {
	case "inviting":
		if group.CreatorUserID == user.ID && group.MemberCount == group.TargetMemberCount {
			lines = append(lines, fmt.Sprintf("\nAll members joined! Send START %s to choose payout rotation.", group.Name))
		} else if group.CreatorUserID == user.ID {
			lines = append(lines, "\nWaiting for more members to join.")
		}
	case "active":
		progress, err := s.store.ThriftCycleProgressForGroup(ctx, group.ID)
		if err == nil {
			lines = append(lines, fmt.Sprintf("\nCycle %d — %d of %d members paid", progress.CycleNumber, progress.PaidCount, progress.TotalMembers))
			lines = append(lines, fmt.Sprintf("Payout to: %s", progress.PayoutMemberName))
			lines = append(lines, fmt.Sprintf("Due: %s", progress.DueAt.Format("02 Jan 2006")))
		}
		lines = append(lines, fmt.Sprintf("\nTo pay: CONTRIBUTE %s", group.Name))
	case "completed":
		lines = append(lines, "\nThis thrift group has completed all cycles.")
	}
	lines = append(lines, "\nView online: "+webLink)
	return s.sendText(ctx, channel, recipient, strings.Join(lines, "\n"))
}

func thriftJoinNameFromInput(input string) (string, bool) {
	upper := strings.ToUpper(strings.TrimSpace(input))
	if strings.HasPrefix(upper, "JOIN ") {
		name := strings.TrimSpace(input[5:])
		return thriftNameFromText(name)
	}
	return "", false
}

func thriftActivateNameFromInput(input string) (string, bool) {
	upper := strings.ToUpper(strings.TrimSpace(input))
	if strings.HasPrefix(upper, "ACTIVATE ") {
		name := strings.TrimSpace(input[9:])
		return thriftNameFromText(name)
	}
	if strings.HasPrefix(upper, "START ") {
		name := strings.TrimSpace(input[6:])
		return thriftNameFromText(name)
	}
	return "", false
}

func thriftContributeNameFromInput(input string) (string, bool) {
	upper := strings.ToUpper(strings.TrimSpace(input))
	if strings.HasPrefix(upper, "CONTRIBUTE ") {
		name := strings.TrimSpace(input[11:])
		return thriftNameFromText(name)
	}
	return "", false
}

func thriftNameFromText(input string) (string, bool) {
	name := strings.TrimSpace(input)
	name = strings.Trim(name, ".,;: ")
	if len([]rune(name)) >= 2 {
		return name, true
	}
	return "", false
}

func parseRotationIndexes(input string) []int {
	cleaned := strings.NewReplacer(",", " ", ";", " ", "-", " ").Replace(input)
	fields := strings.Fields(cleaned)
	indexes := make([]int, 0, len(fields))
	for _, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil {
			return nil
		}
		indexes = append(indexes, value)
	}
	return indexes
}

func displayNameOrFallback(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	return fallback
}

type thriftConcatResult struct {
	Name      string
	Amount    string
	Frequency string
	Target    string
	Errors    []string
}

func parseCommaSeparatedFields(input string) []string {
	parts := strings.Split(input, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func parseThriftConcatInput(input string, minKobo, maxKobo int64) thriftConcatResult {
	fields := parseCommaSeparatedFields(input)
	result := thriftConcatResult{}

	for _, field := range fields {
		lower := strings.ToLower(field)
		if result.Frequency == "" && (lower == "weekly" || lower == "monthly") {
			result.Frequency = lower
			continue
		}
		if result.Amount == "" {
			if _, err := domain.ParseNGNAmount(field, minKobo, maxKobo); err == nil {
				result.Amount = field
				continue
			}
		}
	}

	var targetCandidates []string
	for _, field := range fields {
		if strings.ToLower(field) == result.Frequency || field == result.Amount {
			continue
		}
		if target, err := strconv.Atoi(field); err == nil && target >= 2 && target <= 12 {
			targetCandidates = append(targetCandidates, field)
		}
	}
	if len(targetCandidates) == 1 {
		result.Target = targetCandidates[0]
	}

	for _, field := range fields {
		if field == result.Frequency || field == result.Amount || field == result.Target {
			continue
		}
		if result.Name == "" {
			result.Name = field
		}
	}

	if result.Name != "" && (len([]rune(result.Name)) < 3 || len([]rune(result.Name)) > 80) {
		result.Errors = append(result.Errors, "Name should be between 3 and 80 characters.")
	}
	if result.Amount != "" {
		if _, err := domain.ParseNGNAmount(result.Amount, minKobo, maxKobo); err != nil {
			result.Errors = append(result.Errors, "Amount: "+err.Error())
		}
	}
	if result.Frequency != "" && result.Frequency != "weekly" && result.Frequency != "monthly" {
		result.Errors = append(result.Errors, "Frequency must be Weekly or Monthly.")
	}
	if result.Target != "" {
		target, err := strconv.Atoi(result.Target)
		if err != nil || target < 2 || target > 12 {
			result.Errors = append(result.Errors, "Member count should be between 2 and 12.")
		}
	}

	return result
}

func (s *ConversationService) sendThriftBankTransferInstructions(ctx context.Context, channel, recipient string, payment store.PaymentView, contribution store.ThriftContributionView, instruction store.BankTransferInstruction) error {
	return s.sendInteractive(ctx, channel, ports.InteractiveMessage{
		To: recipient,
		Body: fmt.Sprintf("Bank transfer details for thrift contribution\n\nGroup: %s\nCycle: %d\nAmount: %s\nBank: %s\nAccount name: %s\nAccount number: %s\nPayment reference: %s\n\nWhat to do:\n1. Open your bank app.\n2. Transfer the exact amount above.\n3. Put the payment reference exactly in narration, remark, or payment reference.\n4. After sending, tap I have transferred.\n\nXego credits the thrift cycle only after this payment is confirmed.",
			contribution.GroupName, contribution.CycleNumber, domain.FormatNGN(payment.AmountKobo), instruction.BankName, instruction.AccountName, instruction.AccountNumber, instruction.SimulatedReference),
		Buttons: []ports.InteractiveButton{
			{ID: "confirm_bank_transfer", Title: "I have transferred"},
			{ID: "cancel_payment", Title: "Cancel"},
		},
	})
}
