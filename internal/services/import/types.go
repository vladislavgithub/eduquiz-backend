// Общие типы для парсеров импорта банков вопросов
// (Moodle XML, Moodle GIFT, в будущем — CSV).
//
// Парсер возвращает срез ParsedQuestion и срез ImportWarning.
// Ошибки самого парсинга (битый формат) — отдельный error.
// Неподдерживаемые типы вопросов превращаются в warning'и
// (мы их пропускаем, но импорт в целом продолжается).
package importpkg

// ParsedQuestion — нейтральный промежуточный формат, который потом
// преобразуется в repository.Question.
//
//	Kind         — наш question_kind: "single_choice" | "multi_choice" |
//	               "true_false" | "open_text" | "numerical".
//	Text         — текст вопроса (без HTML-тегов).
//	Options      — для choice-типов: список (id, text). Для true_false
//	               сами создадим в эндпоинте.
//	Correct      — для choice-типов: id-варианты (["a"], ["a","c"]);
//	               для true_false: ["true"]/["false"];
//	               для open_text: список допустимых текстов;
//	               для numerical: одна строка-число.
//	Difficulty   — 1..5, default 3.
//	TimeLimitSec — лимит времени per-question; default 30.
//	Metadata     — свободный JSON: tolerance (numerical), image_url.
type ParsedQuestion struct {
	Kind         string
	Text         string
	Options      []ParsedOption
	Correct      []string
	Difficulty   int
	TimeLimitSec int
	Metadata     map[string]any
}

type ParsedOption struct {
	ID   string // a, b, c, d, ...
	Text string
}

// ImportWarning — пропущенный/проигнорированный кусок импорта.
// Не блокирует импорт остальных вопросов; накапливается в массиве
// и возвращается клиенту, чтобы преподаватель видел что не загрузилось.
type ImportWarning struct {
	Index   int    // номер исходного вопроса (1-based) или 0 для общих
	Kind    string // "unsupported_type", "invalid", "no_correct_answer"
	Message string // человекочитаемое объяснение
}
