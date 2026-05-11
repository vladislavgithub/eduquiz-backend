// Парсер Moodle Quiz XML.
//
// Поддерживаемые типы:
//   - multichoice (single=true → single_choice; single=false → multi_choice)
//   - truefalse → true_false
//   - shortanswer → open_text
//   - numerical → numerical
//
// Остальные типы (essay, match, cloze, description, calculated…) — warning,
// импорт продолжается.
//
// HTML в текстах: вытаскиваем первый <img src="…"> в metadata.image_url,
// затем чистим все теги. Base64-картинки в <file> элементах пока не
// импортируются (требуют отдельной загрузки через /api/v1/uploads).
package importpkg

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Внутренние XML-структуры. encoding/xml сам распаковывает по тегам.
type moodleQuiz struct {
	XMLName   xml.Name         `xml:"quiz"`
	Questions []moodleQuestion `xml:"question"`
}

type moodleQuestion struct {
	Type         string         `xml:"type,attr"`
	Name         moodleNamedTxt `xml:"name"`
	QuestionText moodleNamedTxt `xml:"questiontext"`
	Single       string         `xml:"single"` // "true" или "false" для multichoice
	Answers      []moodleAnswer `xml:"answer"`
}

type moodleNamedTxt struct {
	Text string `xml:"text"`
}

type moodleAnswer struct {
	Fraction  string `xml:"fraction,attr"`
	Text      string `xml:"text"`
	Tolerance string `xml:"tolerance"` // только для numerical
}

// ParseMoodleXML парсит .xml экспорт банка вопросов Moodle.
// Возвращает список распознанных вопросов и список warning'ов.
// Возвращает error только если XML вообще битый (нельзя его декодировать).
//
// Безопасность: вырезаем DOCTYPE/ENTITY перед парсингом, чтобы исключить
// XXE-атаки (External Entity Expansion / Billion Laughs). encoding/xml в Go
// сам по себе не выполняет внешние сущности, но default'но раскрывает
// внутренние — что делает Billion Laughs возможным.
func ParseMoodleXML(content []byte) ([]ParsedQuestion, []ImportWarning, error) {
	content = stripDoctypeAndEntities(content)
	var quiz moodleQuiz
	if err := xml.Unmarshal(content, &quiz); err != nil {
		return nil, nil, fmt.Errorf("parse moodle xml: %w", err)
	}

	out := make([]ParsedQuestion, 0, len(quiz.Questions))
	warnings := make([]ImportWarning, 0)

	for i, q := range quiz.Questions {
		idx := i + 1 // 1-based для пользовательских сообщений
		text, imageURL := extractTextAndImage(q.QuestionText.Text)
		// Fallback: если questiontext пуст, берём имя.
		if strings.TrimSpace(text) == "" {
			text, _ = extractTextAndImage(q.Name.Text)
		}
		if strings.TrimSpace(text) == "" {
			warnings = append(warnings, ImportWarning{
				Index: idx, Kind: "invalid",
				Message: "пустой текст вопроса",
			})
			continue
		}

		switch strings.ToLower(q.Type) {
		case "multichoice":
			parsed, ok := buildChoice(q, idx, &warnings)
			if !ok {
				continue
			}
			parsed.Text = text
			if imageURL != "" {
				parsed.Metadata = map[string]any{"image_url": imageURL}
			}
			out = append(out, parsed)

		case "truefalse":
			parsed, ok := buildTrueFalse(q, idx, &warnings)
			if !ok {
				continue
			}
			parsed.Text = text
			if imageURL != "" {
				parsed.Metadata = map[string]any{"image_url": imageURL}
			}
			out = append(out, parsed)

		case "shortanswer":
			parsed := buildShortAnswer(q)
			parsed.Text = text
			if imageURL != "" {
				parsed.Metadata = map[string]any{"image_url": imageURL}
			}
			if len(parsed.Correct) == 0 {
				warnings = append(warnings, ImportWarning{
					Index: idx, Kind: "no_correct_answer",
					Message: "shortanswer без правильного ответа",
				})
				continue
			}
			out = append(out, parsed)

		case "numerical":
			parsed, ok := buildNumerical(q, idx, &warnings)
			if !ok {
				continue
			}
			parsed.Text = text
			if imageURL != "" {
				if parsed.Metadata == nil {
					parsed.Metadata = map[string]any{}
				}
				parsed.Metadata["image_url"] = imageURL
			}
			out = append(out, parsed)

		case "essay", "match", "matching", "cloze", "ddwtos", "ddmarker",
			"calculated", "calculatedmulti", "calculatedsimple",
			"description", "category":
			warnings = append(warnings, ImportWarning{
				Index: idx, Kind: "unsupported_type",
				Message: "тип «" + q.Type + "» пока не поддерживается",
			})

		default:
			warnings = append(warnings, ImportWarning{
				Index: idx, Kind: "unsupported_type",
				Message: "неизвестный тип «" + q.Type + "»",
			})
		}
	}

	return out, warnings, nil
}

// buildChoice собирает single/multi из multichoice. single определяется
// по `<single>` ИЛИ по числу правильных вариантов (если single не задан).
func buildChoice(q moodleQuestion, idx int, warnings *[]ImportWarning) (ParsedQuestion, bool) {
	opts := make([]ParsedOption, 0, len(q.Answers))
	correct := make([]string, 0)
	for i, a := range q.Answers {
		text, _ := extractTextAndImage(a.Text)
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		id := letterID(i)
		opts = append(opts, ParsedOption{ID: id, Text: text})
		if isCorrectFraction(a.Fraction) {
			correct = append(correct, id)
		}
	}
	if len(opts) < 2 {
		*warnings = append(*warnings, ImportWarning{
			Index: idx, Kind: "invalid",
			Message: "multichoice: меньше 2 вариантов",
		})
		return ParsedQuestion{}, false
	}
	if len(correct) == 0 {
		*warnings = append(*warnings, ImportWarning{
			Index: idx, Kind: "no_correct_answer",
			Message: "multichoice: нет правильного варианта (fraction>0)",
		})
		return ParsedQuestion{}, false
	}
	kind := "single_choice"
	// `single` определяет режим явно. Если single=false — точно multi.
	// Если не задан — multi только если правильных >1.
	switch strings.ToLower(strings.TrimSpace(q.Single)) {
	case "false":
		kind = "multi_choice"
	case "true":
		kind = "single_choice"
	default:
		if len(correct) > 1 {
			kind = "multi_choice"
		}
	}
	if kind == "single_choice" && len(correct) > 1 {
		// Возьмём первый правильный — лучше так, чем терять вопрос.
		correct = correct[:1]
	}
	return ParsedQuestion{
		Kind:         kind,
		Options:      opts,
		Correct:      correct,
		Difficulty:   3,
		TimeLimitSec: 30,
	}, true
}

func buildTrueFalse(q moodleQuestion, idx int, warnings *[]ImportWarning) (ParsedQuestion, bool) {
	// В truefalse Moodle answers с text="true"/"false" и fraction.
	correctAnswer := ""
	for _, a := range q.Answers {
		if !isCorrectFraction(a.Fraction) {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(a.Text)) {
		case "true", "правда", "верно", "yes", "да":
			correctAnswer = "true"
		case "false", "ложь", "неверно", "no", "нет":
			correctAnswer = "false"
		}
	}
	if correctAnswer == "" {
		*warnings = append(*warnings, ImportWarning{
			Index: idx, Kind: "no_correct_answer",
			Message: "truefalse: не удалось определить правильный ответ",
		})
		return ParsedQuestion{}, false
	}
	return ParsedQuestion{
		Kind:         "true_false",
		Options:      defaultTrueFalseOptions(),
		Correct:      []string{correctAnswer},
		Difficulty:   3,
		TimeLimitSec: 30,
	}, true
}

// defaultTrueFalseOptions — обязательные варианты «Верно/Неверно» для TF.
// Студенту нужны плитки для тапа; парсер импорта без них оставлял пустой
// options[] и UI рендерил ничего. Id совпадают с Correct (true/false).
func defaultTrueFalseOptions() []ParsedOption {
	return []ParsedOption{
		{ID: "true", Text: "Верно"},
		{ID: "false", Text: "Неверно"},
	}
}

func buildShortAnswer(q moodleQuestion) ParsedQuestion {
	syns := make([]string, 0, len(q.Answers))
	for _, a := range q.Answers {
		if !isCorrectFraction(a.Fraction) {
			continue
		}
		t := strings.TrimSpace(a.Text)
		if t == "" {
			continue
		}
		// Moodle часто использует * как wildcard; для нашего exact-match
		// проще убрать * и сравнивать substring'ом. Это approximation,
		// преподаватель может потом поправить руками.
		t = strings.ReplaceAll(t, "*", "")
		t = strings.TrimSpace(t)
		if t != "" {
			syns = append(syns, t)
		}
	}
	return ParsedQuestion{
		Kind:         "open_text",
		Correct:      syns,
		Difficulty:   3,
		TimeLimitSec: 30,
	}
}

func buildNumerical(q moodleQuestion, idx int, warnings *[]ImportWarning) (ParsedQuestion, bool) {
	for _, a := range q.Answers {
		if !isCorrectFraction(a.Fraction) {
			continue
		}
		num, err := strconv.ParseFloat(strings.TrimSpace(a.Text), 64)
		if err != nil {
			continue
		}
		tol := 0.0
		if a.Tolerance != "" {
			if t, err := strconv.ParseFloat(strings.TrimSpace(a.Tolerance), 64); err == nil {
				tol = t
			}
		}
		return ParsedQuestion{
			Kind:    "numerical",
			Correct: []string{strconv.FormatFloat(num, 'f', -1, 64)},
			Metadata: map[string]any{
				"tolerance": tol,
			},
			Difficulty:   3,
			TimeLimitSec: 30,
		}, true
	}
	*warnings = append(*warnings, ImportWarning{
		Index: idx, Kind: "no_correct_answer",
		Message: "numerical: нет правильного числового ответа",
	})
	return ParsedQuestion{}, false
}

// isCorrectFraction — Moodle fraction; считаем правильным любое >0.
// Учитываем что пробелы и проценты бывают: "100", "100.0", "33.33333".
func isCorrectFraction(s string) bool {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return false
	}
	return v > 0
}

// extractTextAndImage чистит HTML из текста и достаёт первый <img src="...">
// в metadata.image_url. Текст возвращается без тегов.
func extractTextAndImage(raw string) (text, imageURL string) {
	// Snapshot первого src в <img> — простой regex без полного HTML-парсера,
	// этого достаточно для типового Moodle-экспорта.
	if m := imgSrcRe.FindStringSubmatch(raw); len(m) > 1 {
		imageURL = m[1]
	}
	// Уберём все теги и лишние пробелы. CDATA уже распакована
	// encoding/xml'ом до прихода сюда.
	text = stripHTML(raw)
	return text, imageURL
}

var imgSrcRe = regexp.MustCompile(`(?i)<img[^>]+src\s*=\s*["']([^"']+)["']`)
var tagRe = regexp.MustCompile(`<[^>]+>`)
var wsRe = regexp.MustCompile(`[\s\xa0]+`)

func stripHTML(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	// HTML-entities: минимум nbsp/amp/lt/gt/quot.
	repl := strings.NewReplacer(
		"&nbsp;", " ",
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
	)
	s = repl.Replace(s)
	s = wsRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// letterID возвращает 'a' для 0, 'b' для 1, ..., 'z' для 25, потом 'aa', 'ab'.
// На практике >26 опций в выборных вопросах не бывает.
func letterID(i int) string {
	if i < 26 {
		return string(rune('a' + i))
	}
	return string(rune('a'+(i/26)-1)) + string(rune('a'+i%26))
}

// stripDoctypeAndEntities удаляет <!DOCTYPE …> и <!ENTITY …> из XML —
// защита от XXE и Billion Laughs. Реальные Moodle-экспорты их не
// используют; кто-то злонамеренный мог бы подложить DTD с миллиардом
// раскрытий и положить парсер.
var docTypeRe = regexp.MustCompile(`(?si)<!DOCTYPE[^>]*(\[[^\]]*\])?\s*>`)
var entityRe = regexp.MustCompile(`(?si)<!ENTITY[^>]*>`)

func stripDoctypeAndEntities(b []byte) []byte {
	b = docTypeRe.ReplaceAll(b, nil)
	b = entityRe.ReplaceAll(b, nil)
	return b
}
