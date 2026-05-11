// Парсер Moodle GIFT (Generic Import Format Template).
// Текстовый формат, описание: https://docs.moodle.org/en/GIFT_format
//
// Поддерживаем:
//
//	Q? {=correct ~wrong1 ~wrong2}             // single_choice
//	Q? {=a =b ~c}                             // multi_choice (несколько =)
//	Q? {TRUE} / {FALSE} / {T} / {F}           // true_false
//	Q? {#42:0.5}                              // numerical (значение:tolerance)
//	Q? {#42}                                  // numerical (точное равенство)
//	Q? {= moscow = msk =Moscow}               // open_text (несколько =синонимов
//	                                          //   когда нет ~wrong — short answer)
//
// Игнорируем: $CATEGORY:, // комментарии, пустые строки. Многострочные
// вопросы — текст до { склеиваем, дальнейшее вне { } игнорируем.
package importpkg

import (
	"strconv"
	"strings"
)

// ParseGIFT принимает текст .gift файла, возвращает распознанные
// вопросы и warning'и. Возвращает error только при катастрофическом
// сбое (на практике parser лоялен и почти не падает).
func ParseGIFT(content string) ([]ParsedQuestion, []ImportWarning, error) {
	out := make([]ParsedQuestion, 0)
	warnings := make([]ImportWarning, 0)

	// Разбиваем на блоки по пустым строкам — каждый блок один вопрос.
	// Внутри блока выкидываем comment-строки и $CATEGORY.
	blocks := splitGIFTBlocks(content)
	for i, block := range blocks {
		idx := i + 1
		text, body, ok := extractTextAndBody(block)
		if !ok || strings.TrimSpace(text) == "" {
			// Пустой/невалидный блок — пропускаем без warning'а.
			continue
		}

		parsed, perr := parseGIFTBody(text, body)
		if perr != "" {
			warnings = append(warnings, ImportWarning{
				Index: idx, Kind: "invalid", Message: perr,
			})
			continue
		}
		out = append(out, parsed)
	}

	return out, warnings, nil
}

// splitGIFTBlocks делит файл на отдельные question-блоки.
// Блок — это один или несколько строк, разделённые пустой строкой.
// Комментарии // ... до конца строки и $CATEGORY: выкидываем.
func splitGIFTBlocks(s string) []string {
	lines := strings.Split(s, "\n")
	var blocks []string
	var cur strings.Builder
	for _, l := range lines {
		t := strings.TrimRight(l, "\r ")
		// Комментарий // — до конца строки.
		if i := strings.Index(t, "//"); i >= 0 {
			t = strings.TrimRight(t[:i], " \t")
		}
		// $CATEGORY: и пустые строки — конец блока.
		if strings.HasPrefix(strings.TrimSpace(t), "$CATEGORY:") {
			if cur.Len() > 0 {
				blocks = append(blocks, cur.String())
				cur.Reset()
			}
			continue
		}
		if strings.TrimSpace(t) == "" {
			if cur.Len() > 0 {
				blocks = append(blocks, cur.String())
				cur.Reset()
			}
			continue
		}
		if cur.Len() > 0 {
			cur.WriteByte(' ')
		}
		cur.WriteString(strings.TrimSpace(t))
	}
	if cur.Len() > 0 {
		blocks = append(blocks, cur.String())
	}
	return blocks
}

// extractTextAndBody вытаскивает текст вопроса (всё до первой `{`)
// и тело ответов (между `{` и `}`). Учитывает экранированные `\{` и `\}`.
func extractTextAndBody(block string) (text, body string, ok bool) {
	// Найти первый неэкранированный `{`.
	open := -1
	for i := 0; i < len(block); i++ {
		if block[i] == '{' && (i == 0 || block[i-1] != '\\') {
			open = i
			break
		}
	}
	if open < 0 {
		return "", "", false
	}
	// Найти соответствующий `}` (последний неэкранированный).
	close := -1
	for i := len(block) - 1; i > open; i-- {
		if block[i] == '}' && (i == 0 || block[i-1] != '\\') {
			close = i
			break
		}
	}
	if close < 0 {
		return "", "", false
	}
	text = unescapeGIFT(strings.TrimSpace(block[:open]))
	body = block[open+1 : close]
	return text, body, true
}

func parseGIFTBody(text, body string) (ParsedQuestion, string) {
	body = strings.TrimSpace(body)

	// True/False: {TRUE}, {T}, {FALSE}, {F}.
	upper := strings.ToUpper(body)
	if upper == "TRUE" || upper == "T" {
		return ParsedQuestion{
			Kind:         "true_false",
			Text:         text,
			Options:      defaultTrueFalseOptions(),
			Correct:      []string{"true"},
			Difficulty:   3,
			TimeLimitSec: 30,
		}, ""
	}
	if upper == "FALSE" || upper == "F" {
		return ParsedQuestion{
			Kind:         "true_false",
			Text:         text,
			Options:      defaultTrueFalseOptions(),
			Correct:      []string{"false"},
			Difficulty:   3,
			TimeLimitSec: 30,
		}, ""
	}

	// Numerical: тело начинается с #.
	if strings.HasPrefix(body, "#") {
		return parseGIFTNumerical(text, body[1:])
	}

	// Choice / open_text: ищем токены `=` (правильный) и `~` (неправильный).
	tokens := splitGIFTAnswers(body)
	if len(tokens) == 0 {
		return ParsedQuestion{}, "пустое тело {} в вопросе"
	}

	var hasWrong bool
	corrects := make([]string, 0)
	options := make([]ParsedOption, 0)
	for _, tk := range tokens {
		switch {
		case strings.HasPrefix(tk, "="):
			val := unescapeGIFT(strings.TrimSpace(tk[1:]))
			val = stripFeedback(val) // убрать `#feedback`
			if val == "" {
				continue
			}
			id := letterID(len(options))
			options = append(options, ParsedOption{ID: id, Text: val})
			corrects = append(corrects, id)
		case strings.HasPrefix(tk, "~"):
			hasWrong = true
			val := unescapeGIFT(strings.TrimSpace(tk[1:]))
			val = stripFeedback(val)
			if val == "" {
				continue
			}
			id := letterID(len(options))
			options = append(options, ParsedOption{ID: id, Text: val})
		}
	}

	// Если нет ~wrong, то это open_text-style: список синонимов.
	if !hasWrong {
		syns := make([]string, 0, len(options))
		for _, o := range options {
			syns = append(syns, o.Text)
		}
		if len(syns) == 0 {
			return ParsedQuestion{}, "не удалось определить правильный ответ"
		}
		return ParsedQuestion{
			Kind:         "open_text",
			Text:         text,
			Correct:      syns,
			Difficulty:   3,
			TimeLimitSec: 30,
		}, ""
	}

	if len(corrects) == 0 {
		return ParsedQuestion{}, "нет правильного варианта (=)"
	}
	if len(options) < 2 {
		return ParsedQuestion{}, "меньше 2 вариантов"
	}

	kind := "single_choice"
	if len(corrects) > 1 {
		kind = "multi_choice"
	}
	return ParsedQuestion{
		Kind:         kind,
		Text:         text,
		Options:      options,
		Correct:      corrects,
		Difficulty:   3,
		TimeLimitSec: 30,
	}, ""
}

// parseGIFTNumerical парсит тело после `#`. Форматы:
//
//	N             — точное равенство.
//	N:T           — N с tolerance T.
//	N..M          — диапазон (берём середину как N, half-range как T).
func parseGIFTNumerical(text, body string) (ParsedQuestion, string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return ParsedQuestion{}, "numerical: пустое значение"
	}
	// Диапазон N..M
	if i := strings.Index(body, ".."); i > 0 {
		a, errA := strconv.ParseFloat(strings.TrimSpace(body[:i]), 64)
		b, errB := strconv.ParseFloat(strings.TrimSpace(body[i+2:]), 64)
		if errA == nil && errB == nil {
			mid := (a + b) / 2
			tol := (b - a) / 2
			if tol < 0 {
				tol = -tol
			}
			return ParsedQuestion{
				Kind:         "numerical",
				Text:         text,
				Correct:      []string{strconv.FormatFloat(mid, 'f', -1, 64)},
				Metadata:     map[string]any{"tolerance": tol},
				Difficulty:   3,
				TimeLimitSec: 30,
			}, ""
		}
	}
	// N:T или просто N
	parts := strings.SplitN(body, ":", 2)
	val, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return ParsedQuestion{}, "numerical: некорректное число"
	}
	tol := 0.0
	if len(parts) == 2 {
		t, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err == nil {
			tol = t
		}
	}
	return ParsedQuestion{
		Kind:         "numerical",
		Text:         text,
		Correct:      []string{strconv.FormatFloat(val, 'f', -1, 64)},
		Metadata:     map[string]any{"tolerance": tol},
		Difficulty:   3,
		TimeLimitSec: 30,
	}, ""
}

// splitGIFTAnswers режет тело на токены вида `=text` / `~text`,
// уважая экранирование `\=`, `\~`, `\{`, `\}`.
func splitGIFTAnswers(body string) []string {
	var tokens []string
	var cur strings.Builder
	prevEsc := false
	flush := func() {
		t := strings.TrimSpace(cur.String())
		if t != "" {
			tokens = append(tokens, t)
		}
		cur.Reset()
	}
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if prevEsc {
			cur.WriteByte(ch)
			prevEsc = false
			continue
		}
		if ch == '\\' {
			prevEsc = true
			cur.WriteByte(ch) // оставляем для unescape позже
			continue
		}
		if ch == '=' || ch == '~' {
			if cur.Len() > 0 {
				flush()
			}
			cur.WriteByte(ch)
			continue
		}
		cur.WriteByte(ch)
	}
	flush()
	return tokens
}

func unescapeGIFT(s string) string {
	repl := strings.NewReplacer(
		`\=`, "=",
		`\~`, "~",
		`\{`, "{",
		`\}`, "}",
		`\#`, "#",
		`\:`, ":",
		`\\`, `\`,
	)
	return repl.Replace(s)
}

// stripFeedback убирает `#feedback` хвост из варианта ответа.
// Пример: `=correct#good job` → `correct`. Не путать с экранированным `\#`.
func stripFeedback(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && (i == 0 || s[i-1] != '\\') {
			return strings.TrimSpace(s[:i])
		}
	}
	return strings.TrimSpace(s)
}
