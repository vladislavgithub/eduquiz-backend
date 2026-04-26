package services

import (
	"encoding/json"
	"testing"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestCheckAnswer_SingleChoice(t *testing.T) {
	correct := raw(`["a"]`)

	if got, ok := CheckAnswer("single_choice", correct, raw(`"a"`)); !ok || !got {
		t.Errorf("plain string match: got=%v ok=%v", got, ok)
	}
	if got, ok := CheckAnswer("single_choice", correct, raw(`"b"`)); !ok || got {
		t.Errorf("plain string mismatch: got=%v ok=%v", got, ok)
	}
	// Поддержка формата {"option_id": "..."} для удобства клиента.
	if got, ok := CheckAnswer("single_choice", correct, raw(`{"option_id":"a"}`)); !ok || !got {
		t.Errorf("object form: got=%v ok=%v", got, ok)
	}
}

func TestCheckAnswer_MultiChoice(t *testing.T) {
	correct := raw(`["a","b","c"]`)

	if got, _ := CheckAnswer("multi_choice", correct, raw(`["c","b","a"]`)); !got {
		t.Error("any-order match failed")
	}
	if got, _ := CheckAnswer("multi_choice", correct, raw(`["a","b"]`)); got {
		t.Error("partial subset must NOT count as correct")
	}
	if got, _ := CheckAnswer("multi_choice", correct, raw(`["a","b","c","d"]`)); got {
		t.Error("superset must NOT count as correct")
	}
}

func TestCheckAnswer_OpenText(t *testing.T) {
	// Шаблон-массив: любое из значений считается корректным.
	correct := raw(`["MTBF","Mean Time Between Failures"]`)

	if got, _ := CheckAnswer("open_text", correct, raw(`"mtbf"`)); !got {
		t.Error("case-insensitive match failed")
	}
	if got, _ := CheckAnswer("open_text", correct, raw(`" Mean Time Between Failures "`)); !got {
		t.Error("trimmed match failed")
	}
	if got, _ := CheckAnswer("open_text", correct, raw(`"MTTR"`)); got {
		t.Error("wrong text must NOT match")
	}
}

func TestCheckAnswer_OpenTextWithoutTemplate(t *testing.T) {
	if _, ok := CheckAnswer("open_text", raw(`null`), raw(`"anything"`)); ok {
		t.Error("open_text without template must yield ok=false (manual review)")
	}
}

func TestCheckAnswer_RatingAndQnA(t *testing.T) {
	if _, ok := CheckAnswer("rating", raw(`null`), raw(`5`)); ok {
		t.Error("rating must yield ok=false")
	}
	if _, ok := CheckAnswer("qna", raw(`null`), raw(`"вопрос"`)); ok {
		t.Error("qna must yield ok=false")
	}
}
