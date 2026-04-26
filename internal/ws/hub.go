// Hub — центральный диспетчер WebSocket-клиентов.
//
// Архитектура: одна горутина-«реактор» на весь хаб владеет картой
// rooms→clients и никаких блокировок не использует — все мутации
// (register/unregister/broadcast) идут через каналы. Клиенты только
// читают/пишут в свои собственные write-каналы; при заполнении канала
// клиент отключается, чтобы медленный участник не блокировал хаб.
//
// Этого достаточно до десятков тысяч одновременных соединений на
// одном инстансе. Для горизонтального масштабирования планируется
// Redis pub/sub поверх Hub.Broadcast (см. docs/архитектура).
package ws

import (
	"encoding/json"
	"log/slog"
	"sync/atomic"

	"github.com/google/uuid"
)

// Event — то, что хаб передаёт клиенту: тип события + произвольный payload.
type Event struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

// Hub — диспетчер. Должен быть запущен через Run в фоновой горутине.
type Hub struct {
	register   chan *Client
	unregister chan *Client
	broadcast  chan broadcastReq

	// Только для метрик: число живых соединений.
	connCount atomic.Int64

	logger *slog.Logger
}

type broadcastReq struct {
	roomID  uuid.UUID
	payload []byte
}

// NewHub создаёт хаб. Не запускает реактор — это задача вызывающего
// (см. Run).
func NewHub(logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		register:   make(chan *Client, 64),
		unregister: make(chan *Client, 64),
		broadcast:  make(chan broadcastReq, 256),
		logger:     logger,
	}
}

// Run запускает реактор хаба. Возвращается, когда канал register закрыт.
// Обычно вызывается так: `go hub.Run()`.
func (h *Hub) Run() {
	rooms := make(map[uuid.UUID]map[*Client]struct{})

	for {
		select {
		case c, ok := <-h.register:
			if !ok {
				return
			}
			set := rooms[c.roomID]
			if set == nil {
				set = make(map[*Client]struct{})
				rooms[c.roomID] = set
			}
			set[c] = struct{}{}
			h.connCount.Add(1)
			h.logger.Debug("ws client registered", "room", c.roomID, "user", c.userID, "conns", h.connCount.Load())

		case c := <-h.unregister:
			if set, ok := rooms[c.roomID]; ok {
				if _, ok := set[c]; ok {
					delete(set, c)
					close(c.send)
					h.connCount.Add(-1)
					if len(set) == 0 {
						delete(rooms, c.roomID)
					}
				}
			}
			h.logger.Debug("ws client unregistered", "room", c.roomID, "user", c.userID, "conns", h.connCount.Load())

		case req := <-h.broadcast:
			set := rooms[req.roomID]
			for c := range set {
				select {
				case c.send <- req.payload:
				default:
					// Клиент не успевает читать — отключаем, чтобы не блокировать хаб.
					delete(set, c)
					close(c.send)
					h.connCount.Add(-1)
					h.logger.Warn("ws slow client dropped", "room", c.roomID, "user", c.userID)
				}
			}
		}
	}
}

// Broadcast реализует handlers.Broadcaster: рассылает событие всем
// клиентам в комнате. Безопасно вызывать из любой горутины.
func (h *Hub) Broadcast(roomID uuid.UUID, event string, payload any) {
	body, err := json.Marshal(Event{Type: event, Payload: payload})
	if err != nil {
		h.logger.Error("ws marshal event", "err", err, "type", event)
		return
	}
	select {
	case h.broadcast <- broadcastReq{roomID: roomID, payload: body}:
	default:
		h.logger.Warn("ws broadcast queue full", "room", roomID, "type", event)
	}
}

// ConnCount возвращает текущее число живых соединений (для /readyz, метрик).
func (h *Hub) ConnCount() int64 { return h.connCount.Load() }
