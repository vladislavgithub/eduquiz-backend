// Пакет mailer отправляет письма через SMTP, используя только stdlib
// (net/smtp + crypto/tls). Никаких внешних зависимостей.
//
// Безопасный деплой без кредов: если SMTPHost/SMTPUsername пусты, любая
// отправка превращается в no-op с warning-логом. Это позволяет выкатить
// код раньше, чем на сервере появятся настройки Yandex SMTP — и начать
// реально слать письма сразу после заполнения env-переменных.
package mailer

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"mime"
	"net/smtp"
	"strings"
)

// Mailer хранит настройки SMTP-подключения.
type Mailer struct {
	host     string
	port     int
	username string
	password string
	from     string
	log      *slog.Logger
}

// New создаёт Mailer. log может быть nil — тогда используется slog.Default().
func New(host string, port int, username, password, from string, log *slog.Logger) *Mailer {
	if log == nil {
		log = slog.Default()
	}
	return &Mailer{
		host:     host,
		port:     port,
		username: username,
		password: password,
		from:     from,
		log:      log,
	}
}

// SendPasswordReset отправляет письмо со ссылкой для сброса пароля.
//
// Если SMTP не сконфигурирован (нет host/username) — это no-op: пишем
// warning и возвращаем nil, чтобы flow восстановления пароля всё равно
// отвечал успехом до того, как креды появятся на сервере.
func (m *Mailer) SendPasswordReset(ctx context.Context, to, resetLink string) error {
	if m.host == "" || m.username == "" {
		m.log.Warn("smtp not configured, skipping email", "to", to)
		return nil
	}

	const subject = "Восстановление пароля EduQuiz"
	body := "Здравствуйте!\r\n\r\n" +
		"Вы запросили восстановление пароля в EduQuiz.\r\n" +
		"Перейдите по ссылке, чтобы задать новый пароль:\r\n\r\n" +
		resetLink + "\r\n\r\n" +
		"Ссылка действительна около 30 минут.\r\n" +
		"Если вы не запрашивали сброс пароля, просто проигнорируйте это письмо.\r\n"

	// Собираем RFC 5322 сообщение с UTF-8 заголовками.
	var msg strings.Builder
	msg.WriteString("From: " + m.from + "\r\n")
	msg.WriteString("To: " + to + "\r\n")
	msg.WriteString("Subject: " + mimeEncode(subject) + "\r\n")
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	msg.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)

	if err := m.send(ctx, to, []byte(msg.String())); err != nil {
		return fmt.Errorf("send password reset email to %s: %w", to, err)
	}
	return nil
}

// send устанавливает SMTP-соединение и отправляет одно письмо.
// Порт 465 => implicit TLS (tls.Dial). Иначе => plain dial + STARTTLS.
func (m *Mailer) send(ctx context.Context, to string, msg []byte) error {
	addr := fmt.Sprintf("%s:%d", m.host, m.port)
	tlsCfg := &tls.Config{ServerName: m.host}
	auth := smtp.PlainAuth("", m.username, m.password, m.host)

	var client *smtp.Client
	if m.port == 465 {
		// Implicit TLS: сразу заворачиваем соединение в TLS.
		conn, err := tls.Dial("tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("tls dial %s: %w", addr, err)
		}
		c, err := smtp.NewClient(conn, m.host)
		if err != nil {
			conn.Close()
			return fmt.Errorf("smtp new client: %w", err)
		}
		client = c
	} else {
		// STARTTLS: обычное подключение, затем апгрейд до TLS.
		c, err := smtp.Dial(addr)
		if err != nil {
			return fmt.Errorf("smtp dial %s: %w", addr, err)
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			c.Close()
			return fmt.Errorf("starttls: %w", err)
		}
		client = c
	}
	defer client.Close()

	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := client.Mail(m.from); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close body: %w", err)
	}
	return client.Quit()
}

// mimeEncode кодирует заголовок Subject в RFC 2047 base64 UTF-8,
// чтобы кириллица корректно отображалась в почтовых клиентах.
func mimeEncode(s string) string {
	return mime.BEncoding.Encode("UTF-8", s)
}
