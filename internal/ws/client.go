// Client — обёртка над одним WebSocket-соединением.
// Запускает две горутины: read-pump (читает фреймы и игнорирует их —
// клиенты только слушают) и write-pump (отдаёт сообщения из канала send).
// Pong/ping-механика держит соединение живым через NAT и обнаруживает
// мёртвые TCP-сокеты в течение pongWait.
package ws

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 4 * 1024
)

// Upgrader — настройки upgrade'а HTTP→WebSocket. CheckOrigin разрешает
// всё: API общается с Flutter Web на разных доменах, а защита идёт
// через JWT в query.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Client — один клиент в комнате.
type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	send   chan []byte
	roomID uuid.UUID
	userID uuid.UUID

	closeOnce sync.Once
	logger    *slog.Logger
}

// serve запускает read и write pumps. Возвращается, когда соединение закрыто.
func (c *Client) serve() {
	go c.writePump()
	c.readPump()
}

func (c *Client) close() {
	c.closeOnce.Do(func() {
		c.hub.unregister <- c
		_ = c.conn.Close()
	})
}

// readPump читает сообщения и обновляет deadline по pong'ам.
// Клиент не может присылать команды через WS (все команды — REST).
func (c *Client) readPump() {
	defer c.close()
	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		// Игнорируем тип/тело — пока что клиент только слушает.
		if _, _, err := c.conn.NextReader(); err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.logger.Warn("ws read error", "err", err)
			}
			return
		}
	}
}

// writePump отдаёт исходящие сообщения и периодические ping'и.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
