// Проверка корректности ответа на вопрос.
// Семантика зависит от типа вопроса (question_kind).
package services

import (
	"encoding/json"
	"sort"
	"strings"
)

// CheckAnswer возвращает (isCorrect, ok). ok=false для типов, у которых
// автоматическая проверка невозможна (open_text без шаблона, qna);
// в этом случае isCorrect не имеет смысла, хендлер сохранит nil.
//
//	kind     — тип вопроса (single_choice / multi_choice / open_text / rating / qna);
//	correct  — поле questions.correct: для choice-типов это массив id вариантов,
//	           для open_text — строка-шаблон (или массив допустимых ответов);
//	value    — значение ответа от клиента.
func CheckAnswer(kind string, correct, value json.RawMessage) (isCorrect bool, ok bool) {
	switch kind {
	case "single_choice":
		return checkSingleChoice(correct, value), true
	case "multi_choice":
		return checkMultiChoice(correct, value), true
	case "open_text":
		return checkOpenText(correct, value)
	case "rating", "qna":
		// Для этих типов нет «правильного» ответа.
		return false, false
	default:
		return false, false
	}
}

func checkSingleChoice(correct, value json.RawMessage) bool {
	var want []string
	var got string

	if err := json.Unmarshal(correct, &want); err != nil || len(want) == 0 {
		return false
	}
	if err := json.Unmarshal(value, &got); err != nil {
		// Возможно, клиент прислал {"option_id": "..."} — попробуем.
		var obj struct {
			OptionID string `json:"option_id"`
		}
		if err := json.Unmarshal(value, &obj); err != nil || obj.OptionID == "" {
			return false
		}
		got = obj.OptionID
	}
	return got == want[0]
}

func checkMultiChoice(correct, value json.RawMessage) bool {
	var want, got []string
	if err := json.Unmarshal(correct, &want); err != nil {
		return false
	}
	if err := json.Unmarshal(value, &got); err != nil {
		return false
	}
	if len(want) != len(got) {
		return false
	}
	sort.Strings(want)
	sort.Strings(got)
	for i := range want {
		if want[i] != got[i] {
			return false
		}
	}
	return true
}

// checkOpenText — простой контрольный механизм:
//   - если correct — массив строк, ищем совпадение (case-insensitive, trimmed);
//   - если correct — одна строка, сравниваем с ней;
//   - если correct отсутствует/null — ok=false (ручная проверка).
func checkOpenText(correct, value json.RawMessage) (bool, bool) {
	if len(correct) == 0 || string(correct) == "null" {
		return false, false
	}
	var got string
	if err := json.Unmarshal(value, &got); err != nil {
		return false, true
	}
	got = strings.ToLower(strings.TrimSpace(got))

	var arr []string
	if err := json.Unmarshal(correct, &arr); err == nil {
		for _, w := range arr {
			if strings.EqualFold(strings.TrimSpace(w), got) {
				return true, true
			}
		}
		return false, true
	}
	var single string
	if err := json.Unmarshal(correct, &single); err == nil {
		return strings.EqualFold(strings.TrimSpace(single), got), true
	}
	return false, true
}
