package importpkg

import (
	"strings"
	"testing"
)

func TestParseMoodleXML_FourTypes(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<quiz>
  <question type="multichoice">
    <name><text>Q1</text></name>
    <questiontext format="html"><text><![CDATA[<p>Какой цвет неба?</p>]]></text></questiontext>
    <single>true</single>
    <answer fraction="100"><text>Голубой</text></answer>
    <answer fraction="0"><text>Красный</text></answer>
    <answer fraction="0"><text>Зелёный</text></answer>
  </question>
  <question type="multichoice">
    <name><text>Q2</text></name>
    <questiontext><text>Чётные:</text></questiontext>
    <single>false</single>
    <answer fraction="50"><text>2</text></answer>
    <answer fraction="0"><text>3</text></answer>
    <answer fraction="50"><text>4</text></answer>
  </question>
  <question type="truefalse">
    <name><text>Q3</text></name>
    <questiontext><text>2+2=4</text></questiontext>
    <answer fraction="100"><text>true</text></answer>
    <answer fraction="0"><text>false</text></answer>
  </question>
  <question type="shortanswer">
    <name><text>Q4</text></name>
    <questiontext><text>Столица России?</text></questiontext>
    <answer fraction="100"><text>Москва</text></answer>
    <answer fraction="100"><text>Moscow</text></answer>
  </question>
  <question type="numerical">
    <name><text>Q5</text></name>
    <questiontext><text>Корень из 16?</text></questiontext>
    <answer fraction="100"><text>4</text><tolerance>0.5</tolerance></answer>
  </question>
</quiz>`
	qs, warns, err := ParseMoodleXML([]byte(xml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(qs) != 5 {
		t.Fatalf("expected 5 questions, got %d (warnings: %+v)", len(qs), warns)
	}
	// Q1: single_choice
	if qs[0].Kind != "single_choice" || qs[0].Correct[0] != "a" || len(qs[0].Options) != 3 {
		t.Errorf("Q1 single_choice incorrect: %+v", qs[0])
	}
	if !strings.Contains(qs[0].Text, "цвет неба") {
		t.Errorf("Q1 text not stripped from HTML: %q", qs[0].Text)
	}
	// Q2: multi_choice (single=false)
	if qs[1].Kind != "multi_choice" {
		t.Errorf("Q2 expected multi_choice, got %s", qs[1].Kind)
	}
	if len(qs[1].Correct) != 2 || qs[1].Correct[0] != "a" || qs[1].Correct[1] != "c" {
		t.Errorf("Q2 correct expected [a,c], got %v", qs[1].Correct)
	}
	// Q3: true_false
	if qs[2].Kind != "true_false" || qs[2].Correct[0] != "true" {
		t.Errorf("Q3 truefalse incorrect: %+v", qs[2])
	}
	// Q4: open_text с двумя синонимами
	if qs[3].Kind != "open_text" || len(qs[3].Correct) != 2 {
		t.Errorf("Q4 shortanswer incorrect: %+v", qs[3])
	}
	// Q5: numerical с tolerance
	if qs[4].Kind != "numerical" {
		t.Errorf("Q5 numerical incorrect kind: %s", qs[4].Kind)
	}
	if tol, ok := qs[4].Metadata["tolerance"].(float64); !ok || tol != 0.5 {
		t.Errorf("Q5 tolerance: %v", qs[4].Metadata)
	}
}

func TestParseMoodleXML_ImageInText(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<quiz>
  <question type="multichoice">
    <questiontext format="html"><text><![CDATA[<p>Что на картинке?</p><img src="https://example.com/cat.png" alt="кот"/>]]></text></questiontext>
    <single>true</single>
    <answer fraction="100"><text>Кот</text></answer>
    <answer fraction="0"><text>Собака</text></answer>
  </question>
</quiz>`
	qs, _, err := ParseMoodleXML([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 {
		t.Fatalf("expected 1 question, got %d", len(qs))
	}
	url, ok := qs[0].Metadata["image_url"].(string)
	if !ok || url != "https://example.com/cat.png" {
		t.Errorf("image_url not extracted: %v", qs[0].Metadata)
	}
	if strings.Contains(qs[0].Text, "<img") {
		t.Errorf("text not stripped from HTML: %q", qs[0].Text)
	}
}

func TestParseMoodleXML_UnsupportedType(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<quiz>
  <question type="essay">
    <questiontext><text>Напишите эссе</text></questiontext>
  </question>
  <question type="truefalse">
    <questiontext><text>OK?</text></questiontext>
    <answer fraction="100"><text>true</text></answer>
  </question>
</quiz>`
	qs, warns, err := ParseMoodleXML([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 || qs[0].Kind != "true_false" {
		t.Errorf("expected only truefalse to be imported, got %+v", qs)
	}
	if len(warns) != 1 || warns[0].Kind != "unsupported_type" {
		t.Errorf("expected 1 unsupported_type warning, got %+v", warns)
	}
}

func TestParseMoodleXML_Malformed(t *testing.T) {
	if _, _, err := ParseMoodleXML([]byte("<not-xml>")); err == nil {
		t.Error("expected parse error for malformed XML")
	}
}

func TestStripHTML(t *testing.T) {
	tests := map[string]string{
		`<p>Hello</p>`:                    "Hello",
		`<b>Bold</b> &amp; <i>italic</i>`: "Bold & italic",
		`Line1<br/>Line2`:                 "Line1 Line2",
		`&nbsp;text&nbsp;`:                "text",
	}
	for in, want := range tests {
		if got := stripHTML(in); got != want {
			t.Errorf("stripHTML(%q) = %q, want %q", in, got, want)
		}
	}
}
