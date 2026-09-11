package services

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	mailjet "github.com/mailjet/mailjet-apiv3-go/v4"
)

// mailerHTTPTimeout bounds one Mailjet round-trip.
//
// The SDK is handed no context and, left alone, an http.Client with a zero
// Timeout — so a connection that is accepted and then never answered blocks
// the calling goroutine for as long as the peer keeps the socket open. Every
// alert in this service sits on somebody's critical path (the inbound hold
// alert on the 30-second poll that is the only thing noticing money arrive),
// which makes an unbounded wait not a slow email but a stopped pipeline.
// Failing at 15s and logging it leaves a gap someone can see and recover.
const mailerHTTPTimeout = 15 * time.Second

type EmailService struct {
	client    *mailjet.Client
	fromEmail string
	fromName  string
}

// MailerConfigured reports whether these credentials yield a service that can
// actually send.
//
// Exported because boot has to be able to warn "you configured alert
// recipients and nothing will ever reach them", and a second copy of this
// condition in main.go is a copy that drifts — at which point the warning
// asserts the opposite of the truth about whether anyone is being paged.
func MailerConfigured(apiKey, secretKey string) bool {
	return strings.TrimSpace(apiKey) != "" && strings.TrimSpace(secretKey) != ""
}

func NewEmailService(apiKey, secretKey string) *EmailService {
	if !MailerConfigured(apiKey, secretKey) {
		log.Println("[EmailService] WARNING: Mailjet credentials not configured, emails will be skipped")
		return &EmailService{}
	}
	client := mailjet.NewMailjetClient(strings.TrimSpace(apiKey), strings.TrimSpace(secretKey))
	client.SetClient(&http.Client{Timeout: mailerHTTPTimeout})
	return &EmailService{
		client:    client,
		fromEmail: "support@tsaconnectworld.com",
		fromName:  "TSA Connect",
	}
}

func (s *EmailService) Send(toEmail, toName, subject, title, message, ctaText, ctaURL string) error {
	if s.client == nil {
		log.Printf("[EmailService] skipping email to %s (no client configured)", toEmail)
		return nil
	}

	htmlBody, err := s.buildTemplate(title, message, ctaText, ctaURL)
	if err != nil {
		log.Printf("[EmailService] failed to render email to %s: %v", toEmail, err)
		return fmt.Errorf("failed to render email: %w", err)
	}

	messagesInfo := []mailjet.InfoMessagesV31{
		{
			From: &mailjet.RecipientV31{
				Email: s.fromEmail,
				Name:  s.fromName,
			},
			To: &mailjet.RecipientsV31{
				mailjet.RecipientV31{
					Email: toEmail,
					Name:  toName,
				},
			},
			Subject:  subject,
			HTMLPart: htmlBody,
		},
	}

	messages := mailjet.MessagesV31{Info: messagesInfo}
	if _, err := s.client.SendMailV31(&messages); err != nil {
		log.Printf("[EmailService] failed to send email to %s: %v", toEmail, err)
		return fmt.Errorf("failed to send email: %w", err)
	}

	log.Printf("[EmailService] sent email to %s: %s", toEmail, subject)
	return nil
}

// SendOTP sends a verification code email with a prominent code display.
func (s *EmailService) SendOTP(toEmail, toName, code string) error {
	if s.client == nil {
		log.Printf("[EmailService] skipping OTP email to %s (no client configured)", toEmail)
		return nil
	}

	htmlBody, err := s.buildOTPTemplate(code)
	if err != nil {
		log.Printf("[EmailService] failed to render OTP email to %s: %v", toEmail, err)
		return fmt.Errorf("failed to render email: %w", err)
	}

	messagesInfo := []mailjet.InfoMessagesV31{
		{
			From: &mailjet.RecipientV31{
				Email: s.fromEmail,
				Name:  s.fromName,
			},
			To: &mailjet.RecipientsV31{
				mailjet.RecipientV31{
					Email: toEmail,
					Name:  toName,
				},
			},
			Subject:  "Your TSA Connect verification code",
			HTMLPart: htmlBody,
		},
	}

	messages := mailjet.MessagesV31{Info: messagesInfo}
	if _, err := s.client.SendMailV31(&messages); err != nil {
		log.Printf("[EmailService] failed to send OTP email to %s: %v", toEmail, err)
		return fmt.Errorf("failed to send email: %w", err)
	}

	log.Printf("[EmailService] sent OTP email to %s", toEmail)
	return nil
}

// Every operational email in this service is assembled from text somebody
// else wrote: a P2P counterparty picks their own display name, and a bank
// statement remark is typed by whoever sent the money. Built with fmt.Sprintf
// that text arrives at the recipient as live HTML, which means the sender
// chooses what the email says — hiding the lines that contradict them and
// pointing the button somewhere else. The inbound hold alert is the worst
// case: it is the message an operator reads to decide where real money goes.
//
// So the shells are html/template, parsed once at init. Escaping is then a
// property of the templates rather than a rule every future caller has to
// remember, and the CTA href gets the URL filter too — a javascript: scheme
// there turns the alert itself into the attack.
var (
	otpTemplate = template.Must(template.New("otp").Parse(`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"></head>
<body style="margin: 0; padding: 0; background-color: #f4f4f5; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;">
<table width="100%" cellpadding="0" cellspacing="0" style="background-color: #f4f4f5; padding: 40px 0;">
<tr><td align="center">

<!-- Logo -->
<table width="600" cellpadding="0" cellspacing="0">
	<tr>
		<td style="padding: 0 0 24px 0; text-align: center;">
			<div style="display: inline-block; background-color: #9D6B38; color: #ffffff; font-size: 24px; font-weight: 700; width: 48px; height: 48px; line-height: 48px; border-radius: 12px; text-align: center;">T</div>
		</td>
	</tr>
</table>

<!-- Card -->
<table width="600" cellpadding="0" cellspacing="0" style="background-color: #ffffff; border-radius: 8px; overflow: hidden; border: 1px solid #e5e7eb;">
	<tr>
		<td style="border-top: 4px solid #9D6B38;"></td>
	</tr>
	<tr>
		<td style="padding: 40px 48px;">
			<h2 style="margin: 0 0 12px 0; color: #1f2937; font-size: 20px; font-weight: 700; text-align: center;">Verify your identity</h2>
			<p style="margin: 0 0 32px 0; color: #6b7280; font-size: 15px; line-height: 1.6; text-align: center;">Before you sign in, we need to verify your identity. Enter the following code on the verification page.</p>

			<!-- Code box -->
			<table width="100%" cellpadding="0" cellspacing="0">
				<tr>
					<td align="center">
						<div style="background-color: #f3f4f6; border-radius: 8px; padding: 24px 48px; display: inline-block;">
							<span style="font-size: 36px; font-weight: 700; letter-spacing: 8px; color: #1f2937; font-family: 'Courier New', monospace;">{{.}}</span>
						</div>
					</td>
				</tr>
			</table>

			<p style="margin: 32px 0 0 0; color: #9ca3af; font-size: 13px; line-height: 1.6; text-align: center;">If you did not request this code, you can safely ignore this email. Your verification code expires after 15 minutes.</p>
		</td>
	</tr>
</table>

<!-- Footer -->
<table width="600" cellpadding="0" cellspacing="0">
	<tr>
		<td style="padding: 24px 0; text-align: center;">
			<p style="margin: 0 0 4px 0; color: #1f2937; font-size: 14px; font-weight: 600;">TSA Connect</p>
			<p style="margin: 0; color: #9ca3af; font-size: 12px;">You're receiving this email because of your account on TSA Connect.</p>
		</td>
	</tr>
</table>

</td></tr>
</table>
</body>
</html>`))
	emailTemplate = template.Must(template.New("email").Parse(`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"></head>
<body style="margin: 0; padding: 0; background-color: #f4f4f5; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;">
<table width="100%" cellpadding="0" cellspacing="0" style="background-color: #f4f4f5; padding: 40px 0;">
<tr><td align="center">

<!-- Logo -->
<table width="600" cellpadding="0" cellspacing="0">
	<tr>
		<td style="padding: 0 0 24px 0; text-align: center;">
			<div style="display: inline-block; background-color: #9D6B38; color: #ffffff; font-size: 24px; font-weight: 700; width: 48px; height: 48px; line-height: 48px; border-radius: 12px; text-align: center;">T</div>
		</td>
	</tr>
</table>

<!-- Card -->
<table width="600" cellpadding="0" cellspacing="0" style="background-color: #ffffff; border-radius: 8px; overflow: hidden; border: 1px solid #e5e7eb;">
	<tr>
		<td style="border-top: 4px solid #9D6B38;"></td>
	</tr>
	<tr>
		<td style="padding: 40px 48px;">
			<h2 style="margin: 0 0 16px 0; color: #1f2937; font-size: 20px; font-weight: 700; text-align: center;">{{.Title}}</h2>
			<p style="margin: 0; color: #4b5563; font-size: 15px; line-height: 1.6; text-align: center;">{{range $i, $line := .Lines}}{{if $i}}<br>{{end}}{{$line}}{{end}}</p>
			<table width="100%" cellpadding="0" cellspacing="0">{{if .HasCTA}}
			<tr>
				<td align="center" style="padding: 28px 0 0 0;">
					<a href="{{.CTAURL}}" style="display: inline-block; background-color: #9D6B38; color: #ffffff; padding: 14px 32px; text-decoration: none; border-radius: 6px; font-weight: 600; font-size: 15px;">{{.CTAText}}</a>
				</td>
			</tr>{{end}}</table>
		</td>
	</tr>
</table>

<!-- Footer -->
<table width="600" cellpadding="0" cellspacing="0">
	<tr>
		<td style="padding: 24px 0; text-align: center;">
			<p style="margin: 0 0 4px 0; color: #1f2937; font-size: 14px; font-weight: 600;">TSA Connect</p>
			<p style="margin: 0; color: #9ca3af; font-size: 12px;">You're receiving this email because of your account on TSA Connect. &mdash; support@tsaconnectworld.com</p>
		</td>
	</tr>
</table>

</td></tr>
</table>
</body>
</html>`))
)

// emailShell is the data the generic template renders.
//
// Lines exists because the message is plain text and the template puts it in a
// single <p>, where a newline is whitespace: the hold alert's amount, time,
// reason and reference would run together into one unreadable sentence. The
// split happens here so the <br> is emitted by the template — escaped content
// with markup between it, never escaped markup.
type emailShell struct {
	Title   string
	Lines   []string
	CTAText string
	CTAURL  string
	HasCTA  bool
}

func (s *EmailService) buildOTPTemplate(code string) (string, error) {
	var out strings.Builder
	if err := otpTemplate.Execute(&out, code); err != nil {
		return "", fmt.Errorf("render otp email: %w", err)
	}
	return out.String(), nil
}

func (s *EmailService) buildTemplate(title, message, ctaText, ctaURL string) (string, error) {
	var out strings.Builder
	err := emailTemplate.Execute(&out, emailShell{
		Title:   title,
		Lines:   strings.Split(message, "\n"),
		CTAText: ctaText,
		CTAURL:  ctaURL,
		// Both or neither: a button with no href is dead, and an href with no
		// label is invisible.
		HasCTA: ctaText != "" && ctaURL != "",
	})
	if err != nil {
		return "", fmt.Errorf("render email: %w", err)
	}
	return out.String(), nil
}
