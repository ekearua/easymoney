package service

import (
	"context"
	"fmt"
	"strings"

	"whatsapp-payment-demo/internal/domain"
	"whatsapp-payment-demo/internal/store"
)

// Programme funds listing: the beneficiary-facing view of restricted
// (programme) balances. A customer who holds money granted by a sponsor — the
// Ministry of Agriculture, a state scheme — needs to see three things before
// they try to spend it: how much, when it lapses, and where it may be spent.
// Discovering the restriction at the point of payment is a support call; this
// command is the answer instead.
//
// The listing is read-only. It never moves money, so it is safe to call from
// any idle state, and it reads the same purse rows the spend gate reads, so it
// cannot promise a merchant the gate would refuse.
func (s *ConversationService) sendProgramFunds(ctx context.Context, channel, recipient string, user store.User) error {
	if s.programPurses == nil {
		return s.sendText(ctx, channel, recipient,
			"Programme funds aren't available right now. Type MENU to see your other options.")
	}
	summaries, err := s.programPurses.PurseSummariesForUser(ctx, user.ID)
	if err != nil {
		return err
	}
	return s.sendText(ctx, channel, recipient, formatProgramFunds(summaries))
}

// programmeFundsEmptyCopy is the answer when a customer holds no purse at all.
// It explains what programme money is, because the command is reachable by
// anyone curious enough to tap it.
const programmeFundsEmptyCopy = "You don't have any programme funds right now.\n\n" +
	"Programme funds are paid to you by an organisation (like the Ministry of Agriculture) and can only be spent at the merchants that organisation approves. They appear here as soon as a payment to you lands."

// formatProgramFunds renders the beneficiary's programme balances. Active
// purses come first with their spendable merchants; closed purses are listed
// separately as history, so a returned grant is visible rather than silently
// missing.
func formatProgramFunds(summaries []PurseSummary) string {
	var active, closed []PurseSummary
	for _, summary := range summaries {
		if summary.Purse.Status == store.PurseStatusActive {
			active = append(active, summary)
		} else {
			closed = append(closed, summary)
		}
	}
	if len(active) == 0 {
		if len(closed) == 0 {
			return programmeFundsEmptyCopy
		}
		return "You have no programme funds left.\n\n" + strings.Join(purseHistoryLines(closed), "\n")
	}

	var sb strings.Builder
	sb.WriteString("Programme funds\n\n")
	sb.WriteString("This money can only be spent at the approved merchants below. It can't be withdrawn, sent to another person, or spent anywhere else.\n")
	for _, summary := range active {
		sb.WriteString("\n")
		writeActivePurse(&sb, summary)
	}
	if len(closed) > 0 {
		sb.WriteString("\nReturned or closed:\n")
		sb.WriteString(strings.Join(purseHistoryLines(closed), "\n"))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// writeActivePurse renders one live purse: the grant, its balance, its expiry,
// and — the part that actually prevents a failed payment — the merchants it may
// be spent at.
func writeActivePurse(sb *strings.Builder, summary PurseSummary) {
	purse := summary.Purse
	fmt.Fprintf(sb, "%s\n", programHeading(purse))
	fmt.Fprintf(sb, "Balance: %s\n", domain.FormatNGN(purse.BalanceKobo))
	fmt.Fprintf(sb, "Expires: %s\n", purseExpiryCopy(purse))
	if names := spendableMerchantNames(summary.Merchants, 6); len(names) > 0 {
		fmt.Fprintf(sb, "Spend at: %s\n", strings.Join(names, ", "))
	} else {
		sb.WriteString("Spend at: no merchants are approved for this programme right now\n")
	}
	if purse.BalanceKobo == 0 {
		sb.WriteString("This balance is empty, so there is nothing to spend.\n")
	}
}

// programHeading names the grant. The programme name is used because the money
// came from an organisation the customer recognises, not from Faya; the code
// is the fallback when a name was never set.
func programHeading(purse store.ProgramPurse) string {
	if name := strings.TrimSpace(purse.ProgramName); name != "" {
		return name
	}
	return purse.ProgramCode
}

// purseExpiryCopy states the deadline in the customer's terms. An open-ended
// purse says so explicitly rather than leaving the line blank, because "no
// expiry" is materially different from "expiry unknown".
func purseExpiryCopy(purse store.ProgramPurse) string {
	if purse.ExpiresAt == nil {
		return "no expiry set"
	}
	return purse.ExpiresAt.Format("2 Jan 2006")
}

// spendableMerchantNames lists the merchants a purse may be spent at, capped so
// one long allow-list cannot bury the rest of the message. The overflow count
// is named rather than dropped, so the customer knows the list is incomplete.
func spendableMerchantNames(members []store.ProgramMember, limit int) []string {
	names := make([]string, 0, len(members))
	for _, member := range members {
		name := strings.TrimSpace(member.MerchantName)
		if name == "" {
			name = member.MerchantSlug
		}
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	if limit > 0 && len(names) > limit {
		names = append(names[:limit], fmt.Sprintf("+%d more", len(names)-limit))
	}
	return names
}

// purseHistoryLines summarises returned/closed purses so the customer can see
// where an unspent grant went.
func purseHistoryLines(closed []PurseSummary) []string {
	lines := make([]string, 0, len(closed))
	for _, summary := range closed {
		purse := summary.Purse
		line := fmt.Sprintf("%s: returned %s", programHeading(purse), domain.FormatNGN(purse.ReturnedKobo))
		if purse.ClosedAt != nil {
			line += fmt.Sprintf(" on %s", purse.ClosedAt.Format("2 Jan 2006"))
		}
		lines = append(lines, line)
	}
	return lines
}
