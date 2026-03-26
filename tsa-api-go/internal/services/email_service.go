package services

import (
	"fmt"
	"log"

	mailjet "github.com/mailjet/mailjet-apiv3-go/v4"
)

type EmailService struct {
	client    *mailjet.Client
	fromEmail string
	fromName  string
}

func NewEmailService(apiKey, secretKey string) *EmailService {
	if apiKey == "" || secretKey == "" {
		log.Println("[EmailService] WARNING: Mailjet credentials not configured, emails will be skipped")
		return &EmailService{}
	}
	return &EmailService{
		client:    mailjet.NewMailjetClient(apiKey, secretKey),
		fromEmail: "support@tsaconnectworld.com",
		fromName:  "TSA Connect",
	}
}

func (s *EmailService) Send(toEmail, toName, subject, title, message, ctaText, ctaURL string) error {
	if s.client == nil {
		log.Printf("[EmailService] skipping email to %s (no client configured)", toEmail)
		return nil
	}

	htmlBody := s.buildTemplate(title, message, ctaText, ctaURL)

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
	_, err := s.client.SendMailV31(&messages)
	if err != nil {
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

	htmlBody := s.buildOTPTemplate(code)

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
	_, err := s.client.SendMailV31(&messages)
	if err != nil {
		log.Printf("[EmailService] failed to send OTP email to %s: %v", toEmail, err)
		return fmt.Errorf("failed to send email: %w", err)
	}

	log.Printf("[EmailService] sent OTP email to %s", toEmail)
	return nil
}

func (s *EmailService) buildOTPTemplate(code string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"></head>
<body style="margin: 0; padding: 0; background-color: #f4f4f5; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;">
<table width="100%%" cellpadding="0" cellspacing="0" style="background-color: #f4f4f5; padding: 40px 0;">
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
			<table width="100%%" cellpadding="0" cellspacing="0">
				<tr>
					<td align="center">
						<div style="background-color: #f3f4f6; border-radius: 8px; padding: 24px 48px; display: inline-block;">
							<span style="font-size: 36px; font-weight: 700; letter-spacing: 8px; color: #1f2937; font-family: 'Courier New', monospace;">%s</span>
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
</html>`, code)
}

func (s *EmailService) buildTemplate(title, message, ctaText, ctaURL string) string {
	ctaBlock := ""
	if ctaText != "" && ctaURL != "" {
		ctaBlock = fmt.Sprintf(`
			<tr>
				<td align="center" style="padding: 28px 0 0 0;">
					<a href="%s" style="display: inline-block; background-color: #9D6B38; color: #ffffff; padding: 14px 32px; text-decoration: none; border-radius: 6px; font-weight: 600; font-size: 15px;">%s</a>
				</td>
			</tr>`, ctaURL, ctaText)
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"></head>
<body style="margin: 0; padding: 0; background-color: #f4f4f5; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;">
<table width="100%%" cellpadding="0" cellspacing="0" style="background-color: #f4f4f5; padding: 40px 0;">
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
			<h2 style="margin: 0 0 16px 0; color: #1f2937; font-size: 20px; font-weight: 700; text-align: center;">%s</h2>
			<p style="margin: 0; color: #4b5563; font-size: 15px; line-height: 1.6; text-align: center;">%s</p>
			<table width="100%%" cellpadding="0" cellspacing="0">%s</table>
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
</html>`, title, message, ctaBlock)
}
