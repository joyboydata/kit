package mailer

import (
	"fmt"
	"net/smtp"
)

// Client sends emails. It encapsulates SMTP connection details
// (host, port, credentials, from address) at construction time,
// so callers only need to specify the recipient, subject, and body.
type Client interface {
	// Send delivers an email with the given subject and body to the recipient.
	Send(to, subject, body string) error
}

// SMTPClient sends email via an SMTP server using PLAIN authentication.
type SMTPClient struct {
	from string
	addr string
	auth smtp.Auth
}

// NewSMTPClient creates a new SMTPClient from configuration.
func NewSMTPClient(cfg EmailConfig) *SMTPClient {
	addr := fmt.Sprintf("%s:%s", cfg.SMTPHost, cfg.SMTPPort)
	auth := smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPHost)
	return &SMTPClient{
		from: cfg.FromAddress,
		addr: addr,
		auth: auth,
	}
}

// Send delivers an email with the given subject and body to the recipient.
func (c *SMTPClient) Send(to, subject, body string) error {
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s", c.from, to, subject, body)
	return smtp.SendMail(c.addr, c.auth, c.from, []string{to}, []byte(msg))
}
