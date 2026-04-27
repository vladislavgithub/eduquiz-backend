// Нагрузочный тест: 30 студентов одновременно подключаются к одной
// race-сессии (солло-режим, без таймера) и проходят весь банк
// «Основы теории надёжности» в режиме «отвечу как можно быстрее».
//
// Что меряем:
//   - p50/p95/p99 латентности на каждом этапе (register, login, join,
//     POST /my/answer);
//   - число ошибок и их типы;
//   - суммарное время прохождения сессии 30 студентами.
//
// Запуск (бэкенд должен быть на :8088 или другом, см. -api):
//
//	go run ./cmd/loadtest -n 30
//
// Перед стартом убедись, что seed-данные в базе (`make seed`) — нам
// нужны teacher@eduquiz.ru / teacher123 и хотя бы один банк
// «Основы теории надёжности».
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var (
	apiBase   = flag.String("api", "http://localhost:8088", "API base URL")
	students  = flag.Int("n", 30, "число одновременных студентов")
	bankName  = flag.String("bank", "Основы теории надёжности", "имя банка для сессии")
	teacherEm = flag.String("teacher", "teacher@eduquiz.ru", "email преподавателя")
	teacherPw = flag.String("password", "teacher123", "пароль преподавателя")
	mode      = flag.String("mode", "race", "режим сессии: classic/timer/race")
)

// timed замеряет длительность вызова и пишет в slice.
type timed struct {
	mu      sync.Mutex
	samples []time.Duration
}

func (t *timed) record(d time.Duration) {
	t.mu.Lock()
	t.samples = append(t.samples, d)
	t.mu.Unlock()
}

// pXX — p50/p95/p99 в миллисекундах.
func (t *timed) pXX() (p50, p95, p99 time.Duration, count int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	count = len(t.samples)
	if count == 0 {
		return
	}
	s := append([]time.Duration(nil), t.samples...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	p50 = s[count*50/100]
	p95 = s[count*95/100]
	if p95 == 0 || count*95/100 >= count {
		p95 = s[count-1]
	}
	p99 = s[count*99/100]
	if count*99/100 >= count {
		p99 = s[count-1]
	}
	return
}

type metrics struct {
	register   timed
	login      timed
	join       timed
	myState    timed
	myAnswer   timed
	startRoom  timed
	getRoom    timed
	createRoom timed

	errCount   atomic.Int64
	totalCalls atomic.Int64
	errKinds   sync.Map // map[string]*int64 — ошибки по типам
}

func (m *metrics) recordError(kind string) {
	m.errCount.Add(1)
	v, _ := m.errKinds.LoadOrStore(kind, new(atomic.Int64))
	v.(*atomic.Int64).Add(1)
}

var m metrics

func main() {
	flag.Parse()

	log.Printf("=== loadtest: %d студентов, режим=%s, api=%s ===", *students, *mode, *apiBase)

	// 1. Логинимся как teacher → находим course_id и bank_id.
	teacherToken, err := doLogin(*teacherEm, *teacherPw)
	if err != nil {
		log.Fatalf("не удалось войти как teacher: %v", err)
	}
	courseID, bankID, err := findBank(teacherToken, *bankName)
	if err != nil {
		log.Fatalf("не нашли банк: %v", err)
	}
	log.Printf("teacher OK; course=%s bank=%s", short(courseID), short(bankID))

	// 2. Создаём комнату и сразу её стартуем (без ожидания студентов,
	// так как в race-режиме каждый идёт сам по себе).
	roomID, code, err := createRoom(teacherToken, courseID, bankID, *mode)
	if err != nil {
		log.Fatalf("create room: %v", err)
	}
	log.Printf("room=%s code=%s mode=%s", short(roomID), code, *mode)

	// 3. Регистрируем N студентов параллельно (свежие email-ы),
	// каждый получает свой токен.
	tokens := registerStudents(*students)
	log.Printf("students registered: %d", len(tokens))

	// 4. Каждый студент через свой токен джойнится в комнату.
	type joined struct {
		token         string
		participantID string
		nick          string
	}
	joinedAll := make([]joined, 0, len(tokens))
	var joinMu sync.Mutex
	var jwg sync.WaitGroup
	for i, t := range tokens {
		jwg.Add(1)
		go func(i int, tok string) {
			defer jwg.Done()
			nick := fmt.Sprintf("test-student-%02d", i+1)
			pid, err := joinRoom(tok, code, nick)
			if err != nil {
				m.recordError("join: " + err.Error())
				return
			}
			joinMu.Lock()
			joinedAll = append(joinedAll, joined{token: tok, participantID: pid, nick: nick})
			joinMu.Unlock()
		}(i, t)
	}
	jwg.Wait()
	log.Printf("joined: %d", len(joinedAll))

	// 5. Преподаватель стартует комнату.
	if err := startRoom(teacherToken, roomID); err != nil {
		log.Fatalf("start room: %v", err)
	}
	log.Printf("room started")

	// 6. Каждый студент в своей горутине: GET /my/state, цикл из
	// POST /my/answer пока не finished. Все 30 идут параллельно.
	var swg sync.WaitGroup
	startSession := time.Now()
	finishedCount := atomic.Int64{}
	for _, j := range joinedAll {
		swg.Add(1)
		go func(j joined) {
			defer swg.Done()
			// Получаем стартовое состояние.
			state, err := getMyState(j.token, roomID)
			if err != nil {
				m.recordError("my/state initial: " + err.Error())
				return
			}
			currentQ := state.CurrentQuestion
			for !state.Finished && currentQ != nil {
				// Симулируем «реальное» время на размышление: 100..800 мс.
				thinkMs := 100 + rand.IntN(700)
				time.Sleep(time.Duration(thinkMs) * time.Millisecond)
				// Случайный ответ (30% шанс правильного — не знаем какой,
				// просто берём первый вариант с вероятностью).
				val := pickRandomOption(currentQ)
				ans, err := submitMyAnswer(j.token, roomID, val, thinkMs)
				if err != nil {
					m.recordError("my/answer: " + err.Error())
					return
				}
				state.Finished = ans.Finished
				currentQ = ans.NextQuestion
			}
			finishedCount.Add(1)
		}(j)
	}
	swg.Wait()
	sessionDur := time.Since(startSession)

	// 7. Сводка.
	fmt.Println()
	fmt.Println("=== СВОДКА ===")
	fmt.Printf("Длительность сессии: %s\n", sessionDur.Round(time.Millisecond))
	fmt.Printf("Финишировали: %d / %d\n", finishedCount.Load(), *students)
	fmt.Printf("Ошибок: %d (на %d вызовов = %.2f%%)\n",
		m.errCount.Load(), m.totalCalls.Load(),
		float64(m.errCount.Load())/float64(m.totalCalls.Load()+1)*100)
	fmt.Println()
	fmt.Println("По типам ошибок:")
	m.errKinds.Range(func(k, v any) bool {
		fmt.Printf("  - %s: %d\n", k, v.(*atomic.Int64).Load())
		return true
	})
	fmt.Println()
	fmt.Println("Латентность:")
	printPxx("register     ", &m.register)
	printPxx("login        ", &m.login)
	printPxx("create room  ", &m.createRoom)
	printPxx("get room     ", &m.getRoom)
	printPxx("start room   ", &m.startRoom)
	printPxx("join         ", &m.join)
	printPxx("GET my/state ", &m.myState)
	printPxx("POST my/ansr ", &m.myAnswer)

	if m.errCount.Load() == 0 && finishedCount.Load() == int64(*students) {
		fmt.Println()
		fmt.Println("✓ ВСЁ ОК")
		os.Exit(0)
	}
	fmt.Println()
	fmt.Println("✗ ЕСТЬ ПРОБЛЕМЫ")
	os.Exit(1)
}

func printPxx(label string, t *timed) {
	p50, p95, p99, n := t.pXX()
	if n == 0 {
		fmt.Printf("  %s: нет данных\n", label)
		return
	}
	fmt.Printf("  %s: n=%-4d  p50=%-7s p95=%-7s p99=%s\n",
		label, n, p50.Round(time.Millisecond), p95.Round(time.Millisecond), p99.Round(time.Millisecond))
}

// --- HTTP helpers ---

func doJSON(method, path, token string, body any, out any, t *timed) error {
	m.totalCalls.Add(1)
	var buf io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, *apiBase+path, buf)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	dur := time.Since(start)
	if t != nil {
		t.record(dur)
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, truncate(string(respBody), 200))
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode: %w; body=%s", err, truncate(string(respBody), 100))
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// --- Domain calls ---

func doLogin(email, password string) (string, error) {
	var out struct {
		AccessToken string `json:"access_token"`
	}
	err := doJSON("POST", "/api/v1/auth/login", "",
		map[string]string{"email": email, "password": password}, &out, &m.login)
	return out.AccessToken, err
}

func doRegister(email, password, fullName string) (string, error) {
	var out struct {
		AccessToken string `json:"access_token"`
	}
	err := doJSON("POST", "/api/v1/auth/register", "",
		map[string]string{"email": email, "password": password, "full_name": fullName, "role": "student"},
		&out, &m.register)
	return out.AccessToken, err
}

type courseDTO struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type bankDTO struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func findBank(token, bankTitle string) (string, string, error) {
	var out struct {
		Items []courseDTO `json:"items"`
	}
	if err := doJSON("GET", "/api/v1/courses", token, nil, &out, nil); err != nil {
		return "", "", err
	}
	for _, c := range out.Items {
		var bs struct {
			Items []bankDTO `json:"items"`
		}
		err := doJSON("GET", "/api/v1/courses/"+c.ID+"/banks", token, nil, &bs, nil)
		if err != nil {
			continue
		}
		for _, b := range bs.Items {
			if b.Title == bankTitle {
				return c.ID, b.ID, nil
			}
		}
	}
	return "", "", fmt.Errorf("bank %q not found", bankTitle)
}

func createRoom(token, courseID, bankID, mode string) (string, string, error) {
	var out struct {
		ID   string `json:"id"`
		Code string `json:"code"`
	}
	body := map[string]any{
		"course_id": courseID,
		"bank_id":   bankID,
		"title":     "load test " + time.Now().Format("15:04:05"),
		"mode":      mode,
	}
	err := doJSON("POST", "/api/v1/rooms", token, body, &out, &m.createRoom)
	return out.ID, out.Code, err
}

func startRoom(token, roomID string) error {
	return doJSON("POST", "/api/v1/rooms/"+roomID+"/start", token, nil, nil, &m.startRoom)
}

func registerStudents(n int) []string {
	out := make([]string, 0, n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	stamp := time.Now().UnixNano()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			email := fmt.Sprintf("loadtest-%d-%d@eduquiz.local", stamp, i)
			password := "loadtest123"
			tok, err := doRegister(email, password, "Тестовый "+strconv.Itoa(i))
			if err != nil {
				m.recordError("register: " + err.Error())
				return
			}
			mu.Lock()
			out = append(out, tok)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	return out
}

func joinRoom(token, code, nick string) (string, error) {
	var out struct {
		ParticipantID string `json:"participant_id"`
	}
	err := doJSON("POST", "/api/v1/rooms/join", token,
		map[string]string{"code": code, "nickname": nick}, &out, &m.join)
	return out.ParticipantID, err
}

type optionDTO struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}
type questionDTO struct {
	ID      string      `json:"id"`
	Options []optionDTO `json:"options"`
}
type myStateDTO struct {
	CurrentQuestionIdx int          `json:"current_question_idx"`
	Total              int          `json:"total"`
	Finished           bool         `json:"finished"`
	CurrentQuestion    *questionDTO `json:"current_question,omitempty"`
}
type myAnswerDTO struct {
	Correct      *bool        `json:"correct,omitempty"`
	AwardedXP    int          `json:"awarded_xp"`
	Finished     bool         `json:"finished"`
	NextQuestion *questionDTO `json:"next_question,omitempty"`
}

func getMyState(token, roomID string) (*myStateDTO, error) {
	var out myStateDTO
	if err := doJSON("GET", "/api/v1/rooms/"+roomID+"/my/state", token, nil, &out, &m.myState); err != nil {
		return nil, err
	}
	return &out, nil
}

func submitMyAnswer(token, roomID string, value any, elapsedMs int) (*myAnswerDTO, error) {
	var out myAnswerDTO
	body := map[string]any{"value": value, "elapsed_ms": elapsedMs}
	if err := doJSON("POST", "/api/v1/rooms/"+roomID+"/my/answer", token, body, &out, &m.myAnswer); err != nil {
		return nil, err
	}
	return &out, nil
}

func pickRandomOption(q *questionDTO) string {
	if q == nil || len(q.Options) == 0 {
		return ""
	}
	return q.Options[rand.IntN(len(q.Options))].ID
}

// Чтобы линтер не плакал на неиспользованный пакет.
var _ = url.QueryEscape
