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

func (s *EmailService) buildTemplate(title, message, ctaText, ctaURL string) string {
	ctaBlock := ""
	if ctaText != "" && ctaURL != "" {
		ctaBlock = fmt.Sprintf(`
			<tr>
				<td style="padding: 20px 0 0 0;">
					<a href="%s" style="display: inline-block; background-color: #9D6B38; color: #ffffff; padding: 12px 24px; text-decoration: none; border-radius: 6px; font-weight: 600;">%s</a>
				</td>
			</tr>`, ctaURL, ctaText)
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"></head>
<body style="margin: 0; padding: 0; background-color: #f4f4f5; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;">
<table width="100%%" cellpadding="0" cellspacing="0" style="background-color: #f4f4f5; padding: 40px 0;">
<tr><td align="center">
<table width="600" cellpadding="0" cellspacing="0" style="background-color: #ffffff; border-radius: 8px; overflow: hidden;">
	<tr>
		<td style="background-color: #1f2937; padding: 24px 32px; text-align: center;">
			<h1 style="margin: 0; color: #ffffff; font-size: 20px; font-weight: 700;">TSA Connect</h1>
		</td>
	</tr>
	<tr>
		<td style="padding: 32px;">
			<h2 style="margin: 0 0 16px 0; color: #1f2937; font-size: 18px;">%s</h2>
			<p style="margin: 0; color: #4b5563; font-size: 15px; line-height: 1.6;">%s</p>
			%s
		</td>
	</tr>
	<tr>
		<td style="padding: 24px 32px; background-color: #f9fafb; text-align: center;">
			<p style="margin: 0; color: #9ca3af; font-size: 12px;">TSA Connect &mdash; support@tsaconnectworld.com</p>
		</td>
	</tr>
</table>
</td></tr>
</table>
</body>
</html>`, title, message, ctaBlock)
}
