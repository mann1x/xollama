package whispertimestamps

import (
	"strings"
	"testing"
)

func TestConvertBackendIntervals(t *testing.T) {
	data := []byte(`{"model":{"multilingual":true},"result":{"language":"fr"},"transcription":[
	 {"text":"","tokens":[{"id":50364}]},
	 {"text":" Bonjour,","offsets":{"from":100,"to":950},"tokens":[
	  {"id":10,"offsets":{"from":100,"to":250}},
	  {"id":11,"offsets":{"from":250,"to":600}},
	  {"id":12,"offsets":{"from":600,"to":620}}]},
	 {"text":" été!","offsets":{"from":950,"to":1400},"tokens":[
	  {"id":50256,"offsets":{"from":950,"to":1390}},{"id":50257}]}]}`)
	out, err := Convert(data, 2, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "Bonjour, été!" || out.Language != "fr" || out.Task != "transcribe" || len(out.Words) != 2 || len(out.Segments) != 2 {
		t.Fatalf("response = %+v", out)
	}
	if out.Words[0] != (Word{"Bonjour,", .1, .62}) || out.Words[1] != (Word{"été!", .95, 1.39}) {
		t.Fatalf("word intervals were not taken from tokens: %+v", out.Words)
	}
}

func TestConvertSilenceAndTranslation(t *testing.T) {
	out, err := Convert([]byte(`{"result":{"language":"en"},"transcription":[]}`), 1, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Task != "translate" || out.Text != "" || out.Words == nil || len(out.Words) != 0 || out.Segments != nil {
		t.Fatalf("response = %+v", out)
	}
}

func TestConvertRefusesMissingOrOutOfRangeAlignment(t *testing.T) {
	for _, tokens := range []string{
		`[{"id":10}]`,
		`[{"id":10,"offsets":{"from":-1,"to":100}}]`,
		`[{"id":10,"offsets":{"from":200,"to":100}}]`,
		`[{"id":50256,"offsets":{"from":0,"to":100}}]`,
	} {
		data := `{"transcription":[{"text":" hello","tokens":TOKENS}]}`
		if _, err := Convert([]byte(strings.Replace(data, "TOKENS", tokens, 1)), 1, false, false); err == nil {
			t.Fatalf("accepted tokens %s", tokens)
		}
	}
}

func TestConvertBoundsPaddedLastWindow(t *testing.T) {
	data := []byte(`{"transcription":[
	 {"text":" country","offsets":{"from":31960,"to":34320},"tokens":[{"id":1499,"offsets":{"from":31960,"to":34260}}]},
	 {"text":" padding","tokens":[{"id":10,"offsets":{"from":34000,"to":35000}}]}]}`)
	out, err := Convert(data, 33, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Words) != 1 || out.Words[0] != (Word{"country", 31.96, 33}) || out.Text != "country" || out.Segments[0].End != 33 {
		t.Fatalf("padded window = %+v", out)
	}
}
