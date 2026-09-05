package mailer

// EmailConfig holds SMTP credentials and addressing for outbound email.
type EmailConfig struct {
	NotifyAddress string
	FromAddress   string
	SMTPHost      string
	SMTPPort      string
	SMTPUser      string
	SMTPPassword  string
}
