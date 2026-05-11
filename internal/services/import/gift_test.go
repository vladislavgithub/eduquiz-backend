package importpkg

import (
	"testing"
)

func TestParseGIFT_AllTypes(t *testing.T) {
	src := `
// Demo bank in GIFT format.

What is the capital of France? {=Paris ~London ~Berlin ~Madrid}

The earth is flat. {FALSE}

What is 2+2? {#4}

Give pi to 2 decimal places. {#3.14:0.005}

Which are even numbers? {=2 =4 ~3 ~5}

Capital of Russia? {=Moscow =Москва}

$CATEGORY: $course$/top/Misc

Range numerical: {#10..20}
`
	qs, warns, err := ParseGIFT(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 7 {
		t.Fatalf("expected 7 questions, got %d (warnings %+v)", len(qs), warns)
	}

	if qs[0].Kind != "single_choice" || qs[0].Correct[0] != "a" {
		t.Errorf("Q1 single_choice: %+v", qs[0])
	}
	if qs[1].Kind != "true_false" || qs[1].Correct[0] != "false" {
		t.Errorf("Q2 truefalse: %+v", qs[1])
	}
	if qs[2].Kind != "numerical" || qs[2].Correct[0] != "4" {
		t.Errorf("Q3 numerical: %+v", qs[2])
	}
	if tol, ok := qs[3].Metadata["tolerance"].(float64); !ok || tol != 0.005 {
		t.Errorf("Q4 numerical with tolerance: %+v", qs[3])
	}
	if qs[4].Kind != "multi_choice" || len(qs[4].Correct) != 2 {
		t.Errorf("Q5 multi_choice: %+v", qs[4])
	}
	if qs[5].Kind != "open_text" || len(qs[5].Correct) != 2 {
		t.Errorf("Q6 open_text (synonyms): %+v", qs[5])
	}
	// Range — должен дать mid=15, tol=5
	if qs[6].Kind != "numerical" || qs[6].Correct[0] != "15" {
		t.Errorf("Q7 range numerical: %+v", qs[6])
	}
	if tol, _ := qs[6].Metadata["tolerance"].(float64); tol != 5 {
		t.Errorf("Q7 range tolerance: %v", qs[6].Metadata)
	}
}

func TestParseGIFT_FeedbackStripped(t *testing.T) {
	src := `Capital? {=Paris#good answer ~London#nope}`
	qs, _, err := ParseGIFT(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 || qs[0].Options[0].Text != "Paris" {
		t.Errorf("feedback not stripped: %+v", qs)
	}
}

func TestParseGIFT_Escaping(t *testing.T) {
	src := `Show \{this\} literally? {=yes ~no}`
	qs, _, err := ParseGIFT(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 {
		t.Fatalf("got %d", len(qs))
	}
	if qs[0].Text != "Show {this} literally?" {
		t.Errorf("escaping not unwrapped: %q", qs[0].Text)
	}
}

func TestParseGIFT_TrueShort(t *testing.T) {
	for _, s := range []string{"{T}", "{TRUE}"} {
		qs, _, err := ParseGIFT("Q? " + s)
		if err != nil || len(qs) != 1 || qs[0].Correct[0] != "true" {
			t.Errorf("expected true_false for %s, got %+v", s, qs)
		}
	}
}

func TestParseGIFT_Comments(t *testing.T) {
	src := `// header
What is X? {=A ~B}
// trailing comment after question

// only comments here
`
	qs, _, err := ParseGIFT(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 {
		t.Errorf("expected 1, got %d", len(qs))
	}
}

func TestStripFeedback(t *testing.T) {
	if got := stripFeedback("Paris#feedback"); got != "Paris" {
		t.Errorf("got %q", got)
	}
	if got := stripFeedback("no#hash here"); got != "no" {
		t.Errorf("got %q", got)
	}
	if got := stripFeedback(`escaped \#hash`); got != `escaped \#hash` {
		t.Errorf("escaped: got %q", got)
	}
}
