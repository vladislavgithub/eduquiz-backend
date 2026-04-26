package ws

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
)

// silentLogger — подавляет вывод в тестах.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// receive ждёт сообщение из канала send или таймаутит. Используется
// в тестах вместо реального WebSocket-соединения.
func receive(t *testing.T, ch chan []byte, timeout time.Duration) []byte {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(timeout):
		t.Fatal("timeout waiting for ws event")
		return nil
	}
}

// fakeClient собирает «Client» с уже подключённым каналом send,
// без реального WebSocket'а. Регистрируется в хабе тем же путём,
// что и настоящий.
func fakeClient(hub *Hub, room uuid.UUID) *Client {
	return &Client{
		hub:    hub,
		send:   make(chan []byte, 8),
		roomID: room,
		userID: uuid.New(),
		logger: silentLogger(),
	}
}

func TestHub_BroadcastReachesRoomMembers(t *testing.T) {
	hub := NewHub(silentLogger())
	go hub.Run()

	roomA := uuid.New()
	roomB := uuid.New()

	a1 := fakeClient(hub, roomA)
	a2 := fakeClient(hub, roomA)
	b1 := fakeClient(hub, roomB)

	hub.register <- a1
	hub.register <- a2
	hub.register <- b1

	// Дождаться, чтобы реактор обработал регистрации.
	time.Sleep(20 * time.Millisecond)

	hub.Broadcast(roomA, "ping", map[string]int{"n": 1})

	got1 := receive(t, a1.send, 200*time.Millisecond)
	got2 := receive(t, a2.send, 200*time.Millisecond)

	var ev Event
	if err := json.Unmarshal(got1, &ev); err != nil || ev.Type != "ping" {
		t.Fatalf("a1 message malformed: %s", got1)
	}
	if string(got1) != string(got2) {
		t.Errorf("a1 and a2 received different events:\n  a1=%s\n  a2=%s", got1, got2)
	}

	// b1 в другой комнате — не должен ничего получить.
	select {
	case msg := <-b1.send:
		t.Fatalf("b1 received unexpected: %s", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHub_UnregisterClosesChannel(t *testing.T) {
	hub := NewHub(silentLogger())
	go hub.Run()

	room := uuid.New()
	c := fakeClient(hub, room)
	hub.register <- c
	time.Sleep(10 * time.Millisecond)

	hub.unregister <- c
	time.Sleep(20 * time.Millisecond)

	// После unregister канал закрыт; чтение должно вернуть ok=false без таймаута.
	select {
	case _, ok := <-c.send:
		if ok {
			t.Fatal("send channel was not closed after unregister")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout reading from closed channel")
	}
}

func TestHub_SlowClientGetsDropped(t *testing.T) {
	hub := NewHub(silentLogger())
	go hub.Run()

	room := uuid.New()
	c := fakeClient(hub, room)
	// Узкий буфер 1 — эмулируем медленного клиента.
	c.send = make(chan []byte, 1)
	hub.register <- c
	time.Sleep(10 * time.Millisecond)

	// Засылаем больше, чем влезает в буфер; реактор увидит default-кейс
	// и отключит клиента.
	for i := 0; i < 5; i++ {
		hub.Broadcast(room, "ev", i)
	}
	time.Sleep(50 * time.Millisecond)

	// После сброса — канал закрыт.
	drained := false
	for !drained {
		select {
		case _, ok := <-c.send:
			if !ok {
				drained = true
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("slow client send not closed within timeout")
		}
	}
}
