// Seed: демо-учётки и банк вопросов по теории надёжности.
//
// Идемпотентен: для пользователей — INSERT ... ON CONFLICT DO UPDATE
// (по email). Для курса — drop по (teacher_id, title) и пересоздание,
// что каскадно обновляет банки и вопросы. Безопасно вызывать сколько
// угодно раз, в т.ч. после правки текстов вопросов в этом файле.
//
// Запуск: `go run ./cmd/seed` из корня репо.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vladislavgithub/eduquiz-backend/internal/auth"
	"github.com/vladislavgithub/eduquiz-backend/internal/config"
	"github.com/vladislavgithub/eduquiz-backend/internal/repository"
)

const (
	teacherEmail    = "teacher@eduquiz.ru"
	teacherPassword = "teacher123"
	teacherName     = "Волков Дмитрий Александрович"

	studentEmail    = "student@eduquiz.ru"
	studentPassword = "student123"
	studentName     = "Дворянкин Владислав"

	courseTitle = "Надёжность технических систем"
	courseDesc  = "Демо-курс по дисциплине «Надёжность» Волкова Д.А., " +
		"кафедра АСУ РГУ нефти и газа им. Губкина."
)

type opt struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type question struct {
	bank       string
	text       string
	options    []opt
	correctIDs []string
	topic      string
	difficulty int
	timeSec    int
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	pool, err := repository.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Пользователи. password_hash перегенерируем при каждом запуске —
	// чтобы можно было сменить пароль в этом файле и обновить.
	teacherID, err := upsertUser(ctx, pool, teacherEmail, teacherPassword, teacherName, "teacher")
	if err != nil {
		log.Fatalf("upsert teacher: %v", err)
	}
	if _, err := upsertUser(ctx, pool, studentEmail, studentPassword, studentName, "student"); err != nil {
		log.Fatalf("upsert student: %v", err)
	}

	// Курс: дропаем существующий с тем же titles+teacher и пересоздаём.
	if _, err := pool.Exec(ctx,
		`DELETE FROM courses WHERE teacher_id = $1 AND title = $2`,
		teacherID, courseTitle); err != nil {
		log.Fatalf("drop course: %v", err)
	}

	var courseID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO courses (teacher_id, title, description)
		VALUES ($1, $2, $3) RETURNING id`,
		teacherID, courseTitle, courseDesc).Scan(&courseID); err != nil {
		log.Fatalf("insert course: %v", err)
	}

	// Банки.
	bankIDs := map[string]uuid.UUID{}
	for _, title := range []string{
		"Основы теории надёжности",
		"Резервирование и отказоустойчивость",
	} {
		var bid uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO question_banks (course_id, title)
			VALUES ($1, $2) RETURNING id`, courseID, title).Scan(&bid); err != nil {
			log.Fatalf("insert bank %q: %v", title, err)
		}
		bankIDs[title] = bid
	}

	// Вопросы.
	for _, q := range allQuestions() {
		bid, ok := bankIDs[q.bank]
		if !ok {
			log.Fatalf("unknown bank in seed: %q", q.bank)
		}
		optsJSON, _ := json.Marshal(q.options)
		correctJSON, _ := json.Marshal(q.correctIDs)
		if _, err := pool.Exec(ctx, `
			INSERT INTO questions (bank_id, kind, text, options, correct, difficulty, topic, time_limit_sec)
			VALUES ($1, 'single_choice', $2, $3, $4, $5, $6, $7)`,
			bid, q.text, optsJSON, correctJSON, q.difficulty, q.topic, q.timeSec); err != nil {
			log.Fatalf("insert question: %v", err)
		}
	}

	fmt.Fprintln(os.Stdout, "─────────────────────────────────────────────────────────")
	fmt.Fprintln(os.Stdout, "Seed готов.")
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintf(os.Stdout, "  teacher: %s  /  %s\n", teacherEmail, teacherPassword)
	fmt.Fprintf(os.Stdout, "  student: %s  /  %s\n", studentEmail, studentPassword)
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintf(os.Stdout, "  курс: %s\n", courseTitle)
	fmt.Fprintf(os.Stdout, "  банков: %d, вопросов: %d\n", len(bankIDs), len(allQuestions()))
	fmt.Fprintln(os.Stdout, "─────────────────────────────────────────────────────────")
}

func upsertUser(ctx context.Context, pool *pgxpool.Pool, email, plain, name, role string) (uuid.UUID, error) {
	hash, err := auth.HashPassword(plain)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, full_name, role)
		VALUES ($1, $2, $3, $4::user_role)
		ON CONFLICT (email) DO UPDATE
			SET password_hash = EXCLUDED.password_hash,
			    full_name     = EXCLUDED.full_name,
			    role          = EXCLUDED.role,
			    updated_at    = now()
		RETURNING id`, email, hash, name, role).Scan(&id)
	return id, err
}

func allQuestions() []question {
	return append(append([]question{}, foundationsBank()...), redundancyBank()...)
}

// --- Bank 1: Основы теории надёжности ---

func foundationsBank() []question {
	bank := "Основы теории надёжности"
	mc := func(text, topic string, difficulty int, opts []opt, correctID string) question {
		return question{
			bank: bank, text: text, options: opts, correctIDs: []string{correctID},
			topic: topic, difficulty: difficulty, timeSec: 30,
		}
	}
	return []question{
		mc("Что обозначает аббревиатура MTBF?", "терминология", 2, []opt{
			{"a", "Mean Time Before Failure"},
			{"b", "Mean Time Between Failures"},
			{"c", "Maximum Time Before Failure"},
			{"d", "Minimum Time Between Faults"},
		}, "b"),

		mc("Что такое ВБР?", "терминология", 2, []opt{
			{"a", "Время безотказной работы"},
			{"b", "Вероятность безотказной работы"},
			{"c", "Время безусловной работы"},
			{"d", "Возможность безотказной работы"},
		}, "b"),

		mc("Какой буквой обычно обозначается интенсивность отказов?", "терминология", 2, []opt{
			{"a", "μ (мю)"},
			{"b", "λ (лямбда)"},
			{"c", "σ (сигма)"},
			{"d", "ρ (ро)"},
		}, "b"),

		mc("Какая величина обратна интенсивности отказов λ при экспоненциальной модели?", "формулы", 3, []opt{
			{"a", "MTTR"},
			{"b", "MTBF"},
			{"c", "ВБР"},
			{"d", "k_g"},
		}, "b"),

		mc("Что такое коэффициент готовности (k_g)?", "показатели", 2, []opt{
			{"a", "Доля времени, когда система работоспособна"},
			{"b", "Среднее время до отказа"},
			{"c", "Количество отказов в час"},
			{"d", "Процент брака на выпуске"},
		}, "a"),

		mc("Какой ГОСТ устанавливает термины и определения теории надёжности в РФ?", "стандарты", 3, []opt{
			{"a", "ГОСТ 34.602"},
			{"b", "ГОСТ Р 27.001-2009"},
			{"c", "ГОСТ Р ИСО 9001"},
			{"d", "ГОСТ 19.701"},
		}, "b"),

		mc("Что такое MTTR?", "терминология", 2, []opt{
			{"a", "Mean Time To Restore (Repair) — среднее время восстановления"},
			{"b", "Maximum Time To Reset"},
			{"c", "Mean Time Tolerance Range"},
			{"d", "Minimum Time To Recover"},
		}, "a"),

		mc("Какое распределение наработки до отказа считается базовой моделью в теории надёжности?", "модели", 3, []opt{
			{"a", "Нормальное распределение"},
			{"b", "Экспоненциальное распределение"},
			{"c", "Распределение Пуассона"},
			{"d", "Распределение Стьюдента"},
		}, "b"),

		mc("Какие свойства входят в комплексное понятие «надёжность» по ГОСТ Р 27.001?", "стандарты", 4, []opt{
			{"a", "Только безотказность"},
			{"b", "Только долговечность"},
			{"c", "Безотказность, долговечность, ремонтопригодность, сохраняемость"},
			{"d", "Только сохраняемость"},
		}, "c"),

		mc("Если λ = 1·10⁻⁴ ч⁻¹ (экспоненциальная модель), чему равно MTBF?", "расчёты", 3, []opt{
			{"a", "100 ч"},
			{"b", "1 000 ч"},
			{"c", "10 000 ч"},
			{"d", "100 000 ч"},
		}, "c"),
	}
}

// --- Bank 2: Резервирование и отказоустойчивость ---

func redundancyBank() []question {
	bank := "Резервирование и отказоустойчивость"
	mc := func(text, topic string, difficulty int, opts []opt, correctID string) question {
		return question{
			bank: bank, text: text, options: opts, correctIDs: []string{correctID},
			topic: topic, difficulty: difficulty, timeSec: 35,
		}
	}
	return []question{
		mc("Что такое резервирование в теории надёжности?", "термины", 2, []opt{
			{"a", "Запасные части на складе"},
			{"b", "Включение в систему дублирующих элементов"},
			{"c", "Регулярное обслуживание"},
			{"d", "Архивирование данных"},
		}, "b"),

		mc("ВБР последовательной системы из двух независимых элементов с ВБР p₁ и p₂ равна:", "расчёты", 3, []opt{
			{"a", "p₁ + p₂"},
			{"b", "p₁ · p₂"},
			{"c", "max(p₁, p₂)"},
			{"d", "min(p₁, p₂)"},
		}, "b"),

		mc("При параллельном (нагруженном) резервировании двух одинаковых элементов с ВБР=p, вероятность отказа системы равна:", "расчёты", 4, []opt{
			{"a", "p²"},
			{"b", "2p"},
			{"c", "(1-p)²"},
			{"d", "1 − 2p"},
		}, "c"),

		mc("Что называют «горячим» резервом?", "термины", 2, []opt{
			{"a", "Резерв, требующий времени на запуск"},
			{"b", "Резерв, всегда находящийся в рабочем режиме"},
			{"c", "Резерв, нагревающийся в процессе работы"},
			{"d", "Запасной комплект, хранящийся на складе"},
		}, "b"),

		mc("Какая стратегия репликации БД даёт автоматическое переключение при отказе мастера?", "архитектура", 3, []opt{
			{"a", "Master-only"},
			{"b", "Master-replica с failover"},
			{"c", "Read-only snapshot"},
			{"d", "Single-node с бэкапом"},
		}, "b"),

		mc("Что такое идемпотентность операции?", "архитектура", 3, []opt{
			{"a", "Операция выполняется быстрее при повторе"},
			{"b", "Повторное выполнение даёт тот же результат, что и однократное"},
			{"c", "Операция всегда успешна"},
			{"d", "Операция атомарна"},
		}, "b"),

		mc("Что снижает MTTR (среднее время восстановления)?", "практика", 3, []opt{
			{"a", "Увеличение MTBF"},
			{"b", "Автоматизация диагностики и восстановления"},
			{"c", "Увеличение количества компонентов"},
			{"d", "Длительные регламентные работы"},
		}, "b"),

		mc("Какие метрики снимает мониторинг (Prometheus + Grafana) для оценки надёжности сервиса?", "практика", 2, []opt{
			{"a", "Только данные пользователей"},
			{"b", "Метрики uptime, latency, error rate"},
			{"c", "Только бизнес-метрики"},
			{"d", "Только логи доступа"},
		}, "b"),
	}
}
