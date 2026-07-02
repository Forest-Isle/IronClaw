package tool

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"

	"github.com/Forest-Isle/daimon/internal/netdial"
)

type SendEmailTool struct {
	host     string
	port     int
	username string
	password string
	from     string
	approval bool
	send     func(ctx context.Context, addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

type sendEmailInput struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func NewSendEmailTool(host string, port int, username, password, from string, requiresApproval bool) *SendEmailTool {
	return &SendEmailTool{
		host:     host,
		port:     port,
		username: username,
		password: password,
		from:     from,
		approval: requiresApproval,
		send:     proxySendMail,
	}
}

func (s *SendEmailTool) Name() string           { return "send_email" }
func (s *SendEmailTool) Description() string    { return "Send an email via SMTP." }
func (s *SendEmailTool) RequiresApproval() bool { return s.approval }
func (s *SendEmailTool) IsReadOnly() bool       { return false }

func (s *SendEmailTool) Capabilities() ToolCapabilities {
	return ToolCapabilities{
		IsReadOnly:      false,
		IsDestructive:   false,
		RequiresNetwork: true,
		ApprovalMode:    "auto",
		Reversibility:   "compensable",
	}
}

func (s *SendEmailTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"to": map[string]any{
				"type":        "string",
				"description": "Recipient email address",
			},
			"subject": map[string]any{
				"type":        "string",
				"description": "Email subject",
			},
			"body": map[string]any{
				"type":        "string",
				"description": "Plain text email body",
			},
		},
		"required": []string{"to", "subject", "body"},
	}
}

func (s *SendEmailTool) Execute(ctx context.Context, input []byte) (Result, error) {
	var in sendEmailInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Error: "invalid input: " + err.Error()}, nil
	}

	to := strings.TrimSpace(in.To)
	subject := strings.TrimSpace(in.Subject)
	body := strings.TrimSpace(in.Body)
	if to == "" {
		return Result{Error: "to is required"}, nil
	}
	if !strings.Contains(to, "@") {
		return Result{Error: "to must be a valid email address"}, nil
	}
	if subject == "" {
		return Result{Error: "subject is required"}, nil
	}
	if body == "" {
		return Result{Error: "body is required"}, nil
	}
	// Reject CRLF in header fields to prevent SMTP header injection (e.g. a
	// subject smuggling an extra "\r\nBcc:" header). The body is content after
	// the header/body separator, so line breaks there are fine.
	if strings.ContainsAny(to, "\r\n") || strings.ContainsAny(subject, "\r\n") {
		return Result{Error: "to/subject must not contain line breaks"}, nil
	}

	fromAddr := s.from
	if fromAddr == "" {
		fromAddr = s.username
	}
	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", fromAddr, to, subject, in.Body))
	auth := smtp.PlainAuth("", s.username, s.password, s.host)
	addr := net.JoinHostPort(s.host, strconv.Itoa(s.port))
	if err := s.send(ctx, addr, auth, fromAddr, []string{to}, msg); err != nil {
		return Result{Error: "send email: " + err.Error()}, nil
	}

	return Result{Output: fmt.Sprintf("Email sent to %s", to)}, nil
}

func proxySendMail(ctx context.Context, addr string, a smtp.Auth, from string, to []string, msg []byte) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	conn, err := netdial.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer c.Close()
	if a != nil {
		// Auth is configured: STARTTLS and AUTH are mandatory. Fail closed if a
		// proxy or MITM stripped either capability rather than sending the
		// message (and credentials) over an unauthenticated cleartext session.
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("smtp: server does not advertise STARTTLS; refusing to send over cleartext")
		}
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
		if ok, _ := c.Extension("AUTH"); !ok {
			return fmt.Errorf("smtp: server does not advertise AUTH after STARTTLS")
		}
		if err := c.Auth(a); err != nil {
			return err
		}
	} else if ok, _ := c.Extension("STARTTLS"); ok {
		// No auth configured: still upgrade opportunistically when offered.
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
