package services

import (
	"strings"
	"testing"
)

// Everything below fixes one thing: the body of an operational email is
// assembled from text we do not control. A P2P counterparty picks their own
// display name, and a bank statement remark is typed by whoever sent the
// money. If that text reaches the recipient's mail client as live HTML, the
// sender chooses what the email says — including hiding the parts that
// disagree with them and pointing the link somewhere else. The inbound hold
// alert is the worst case: it is the message an operator reads to decide where
// real money goes.

func TestEmailTemplateEscapesProviderControlledText(t *testing.T) {
	s := &EmailService{}

	body, err := s.buildTemplate(
		`Reason held: <script>alert('xss')</script>`,
		"Received Money from GTBANK|<img src=x onerror=alert(1)>",
		"Review",
		"https://admin.tsa.test/inbound/credits/1",
	)
	if err != nil {
		t.Fatalf("buildTemplate: %v", err)
	}

	// Tag delimiters are the whole question: escaped, the attribute names are
	// inert text an operator simply reads.
	for _, live := range []string{"<script", "</script", "<img"} {
		if strings.Contains(body, live) {
			t.Errorf("markup %q reached the HTML body unescaped\n---\n%s", live, body)
		}
	}
	// Escaped, not dropped: an operator still has to be able to read what the
	// statement actually said.
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("escaped text missing from body\n---\n%s", body)
	}
}

// A CTA URL is an href, the one part of the email an operator is told to
// click. A javascript: or data: scheme there turns the alert into the attack.
func TestEmailTemplateNeutersAHostileCTAURL(t *testing.T) {
	s := &EmailService{}

	body, err := s.buildTemplate("Title", "Message", "Review this payment", "javascript:alert(1)")
	if err != nil {
		t.Fatalf("buildTemplate: %v", err)
	}
	if strings.Contains(body, "javascript:alert") {
		t.Errorf("javascript: URL survived into an href\n---\n%s", body)
	}
}

// The alert body is plain text with line breaks — amount, time, reason and
// reference, one per line. Collapsed into a single run they are still correct
// and much harder to read at 3am, so the template has to turn them into
// breaks rather than swallowing them.
func TestEmailTemplateKeepsLineBreaks(t *testing.T) {
	s := &EmailService{}

	body, err := s.buildTemplate("Title", "Amount: NGN 2,000.00\nReason held: contested", "", "")
	if err != nil {
		t.Fatalf("buildTemplate: %v", err)
	}
	if !strings.Contains(body, "Amount: NGN 2,000.00<br>Reason held: contested") {
		t.Errorf("line break not rendered as <br>\n---\n%s", body)
	}
}

func TestEmailTemplateDropsTheCTABlockWhenThereIsNoLink(t *testing.T) {
	s := &EmailService{}

	body, err := s.buildTemplate("Title", "Message", "", "")
	if err != nil {
		t.Fatalf("buildTemplate: %v", err)
	}
	if strings.Contains(body, "<a href") {
		t.Errorf("rendered a CTA anchor with no link configured\n---\n%s", body)
	}
}

func TestOTPTemplateEscapes(t *testing.T) {
	s := &EmailService{}

	body, err := s.buildOTPTemplate("<script>alert(1)</script>")
	if err != nil {
		t.Fatalf("buildOTPTemplate: %v", err)
	}
	if strings.Contains(body, "<script>") {
		t.Errorf("markup reached the OTP body unescaped\n---\n%s", body)
	}
}

// The boot warning in main.go and NewEmailService must agree on what "we can
// actually send mail" means, or the warning tells operators the opposite of
// the truth. One predicate, used by both.
func TestMailerConfigured(t *testing.T) {
	cases := []struct {
		apiKey, secretKey string
		want              bool
	}{
		{"key", "secret", true},
		{"", "secret", false},
		{"key", "", false},
		{"", "", false},
		{"  ", "secret", false},
	}
	for _, c := range cases {
		if got := MailerConfigured(c.apiKey, c.secretKey); got != c.want {
			t.Errorf("MailerConfigured(%q, %q) = %v, want %v", c.apiKey, c.secretKey, got, c.want)
		}
	}

	// And the constructor must follow it: credentials it rejects must leave a
	// no-op service, never a client that will fail on the first send.
	if svc := NewEmailService("  ", "secret"); svc.client != nil {
		t.Error("NewEmailService built a client from credentials MailerConfigured rejects")
	}
}

// ---------- structured bodies ----------

// A composed body is the only way a caller gets emphasis into an email, and
// the shape is the point: the markup belongs to the template and the text
// never can. Bold is a flag rather than a tag the caller writes, so text that
// came from a submitter or an admin stays escaped even in a span the caller
// chose to embolden.
func TestEmailBodyRendersItsOwnMarkupAndEscapesTheText(t *testing.T) {
	body := EmailBody{
		{EmailText("Hi "), EmailBold(`<script>alert('xss')</script>`)},
		{},
		{EmailText("Approved.")},
	}

	out, err := body.RenderHTML("Title", "", "")
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}

	if !strings.Contains(out, "<strong>") {
		t.Errorf("emphasis did not reach the recipient as markup\n---\n%s", out)
	}
	if strings.Contains(out, "<script") {
		t.Errorf("caller text reached the body as live markup\n---\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("escaped text missing from body\n---\n%s", out)
	}
	// The empty line is the blank line between paragraphs; it has to survive
	// as a break or the paragraphs run together.
	if !strings.Contains(out, "<br><br>") {
		t.Errorf("blank line did not render as a break\n---\n%s", out)
	}
}

// Send's plain-text callers keep what they have: their message is text, its
// newlines become breaks, and nothing in it is markup.
func TestPlainSendBodyStillEscapesEverything(t *testing.T) {
	s := &EmailService{}

	out, err := s.buildTemplate("Title", "Amount: NGN 2,000.00\n<b>not bold</b>", "", "")
	if err != nil {
		t.Fatalf("buildTemplate: %v", err)
	}
	if !strings.Contains(out, "Amount: NGN 2,000.00<br>") {
		t.Errorf("line break not rendered as <br>\n---\n%s", out)
	}
	if strings.Contains(out, "<b>not bold</b>") {
		t.Errorf("plain-text message was rendered as markup\n---\n%s", out)
	}
}
