package helps

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

const wrappedHello = "<USER_REQUEST>\nhello\n</USER_REQUEST>"

func TestMoveAntigravitySystemInstructionToReminder_MovesTextParts(t *testing.T) {
	payload := []byte(`{"request":{"systemInstruction":{"role":"user","parts":[{"text":"A"},{"text":"B"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

	got := MoveAntigravitySystemInstructionToReminder(payload)

	wantStatic := antigravityIdentityPrefix + antigravityEphemeralMessageNote
	if text := ojson.GetBytes(got, "request.systemInstruction.parts.0.text").String(); text != wantStatic {
		t.Fatalf("static system text = %q, want %q", text, wantStatic)
	}
	if n := len(ojson.GetBytes(got, "request.systemInstruction.parts").Array()); n != 1 {
		t.Fatalf("systemInstruction parts = %d, want 1", n)
	}
	if text := ojson.GetBytes(got, "request.contents.0.parts.0.text").String(); text != "<EPHEMERAL_MESSAGE>\n"+AntigravitySystemReminderBasePrefix+"A\n\nB\n</EPHEMERAL_MESSAGE>" {
		t.Fatalf("reminder text = %q", text)
	}
	if text := ojson.GetBytes(got, "request.contents.0.parts.1.text").String(); text != wrappedHello {
		t.Fatalf("user text = %q, want %q", text, wrappedHello)
	}
	if !strings.Contains(string(got), "<EPHEMERAL_MESSAGE>") || strings.Contains(string(got), `\u003c`) {
		t.Fatalf("payload must keep literal reminder tags: %s", got)
	}
}

func TestMoveAntigravitySystemInstructionToReminder_StringInstruction(t *testing.T) {
	payload := []byte(`{"request":{"systemInstruction":"Be brief","contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

	got := MoveAntigravitySystemInstructionToReminder(payload)

	if text := ojson.GetBytes(got, "request.contents.0.parts.0.text").String(); text != "<EPHEMERAL_MESSAGE>\n"+AntigravitySystemReminderBasePrefix+"Be brief\n</EPHEMERAL_MESSAGE>" {
		t.Fatalf("reminder text = %q", text)
	}
	if text := ojson.GetBytes(got, "request.systemInstruction.parts.0.text").String(); !strings.HasPrefix(text, antigravityIdentityPrefix) {
		t.Fatalf("static system text = %q", text)
	}
}

func TestMoveAntigravitySystemInstructionToReminder_SnakeCaseInstruction(t *testing.T) {
	payload := []byte(`{"request":{"system_instruction":{"parts":[{"text":"Be brief"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

	got := MoveAntigravitySystemInstructionToReminder(payload)

	if ojson.GetBytes(got, "request.system_instruction").Exists() {
		t.Fatalf("system_instruction should be removed: %s", got)
	}
	if text := ojson.GetBytes(got, "request.contents.0.parts.0.text").String(); text != "<EPHEMERAL_MESSAGE>\n"+AntigravitySystemReminderBasePrefix+"Be brief\n</EPHEMERAL_MESSAGE>" {
		t.Fatalf("reminder text = %q", text)
	}
	if text := ojson.GetBytes(got, "request.systemInstruction.parts.0.text").String(); !strings.HasPrefix(text, antigravityIdentityPrefix) {
		t.Fatalf("static system text = %q", text)
	}
}

func TestMoveAntigravitySystemInstructionToReminder_SkipsLeadingModelTurn(t *testing.T) {
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Rules"}]},"contents":[{"role":"model","parts":[{"text":"hi"}]},{"role":"user","parts":[{"text":"hello"}]}]}}`)

	got := MoveAntigravitySystemInstructionToReminder(payload)

	if text := ojson.GetBytes(got, "request.contents.0.parts.0.text").String(); text != "hi" {
		t.Fatalf("model turn changed: %q", text)
	}
	if text := ojson.GetBytes(got, "request.contents.1.parts.0.text").String(); text != "<EPHEMERAL_MESSAGE>\n"+AntigravitySystemReminderBasePrefix+"Rules\n</EPHEMERAL_MESSAGE>" {
		t.Fatalf("reminder text = %q", text)
	}
	if text := ojson.GetBytes(got, "request.contents.1.parts.1.text").String(); text != wrappedHello {
		t.Fatalf("user text = %q, want %q", text, wrappedHello)
	}
}

func TestMoveAntigravitySystemInstructionToReminder_PlacesAfterFunctionResponses(t *testing.T) {
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Rules"}]},"contents":[{"role":"user","parts":[{"functionResponse":{"name":"a","response":{}}},{"text":"hello"}]}]}}`)

	got := MoveAntigravitySystemInstructionToReminder(payload)

	if !ojson.GetBytes(got, "request.contents.0.parts.0.functionResponse").Exists() {
		t.Fatalf("functionResponse must stay first: %s", got)
	}
	if text := ojson.GetBytes(got, "request.contents.0.parts.1.text").String(); text != "<EPHEMERAL_MESSAGE>\n"+AntigravitySystemReminderBasePrefix+"Rules\n</EPHEMERAL_MESSAGE>" {
		t.Fatalf("reminder text = %q", text)
	}
	if text := ojson.GetBytes(got, "request.contents.0.parts.2.text").String(); text != wrappedHello {
		t.Fatalf("user text = %q, want %q", text, wrappedHello)
	}
}

func TestMoveAntigravitySystemInstructionToReminder_NoUserTurn(t *testing.T) {
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Rules"}]},"contents":[{"role":"model","parts":[{"text":"hi"}]}]}}`)

	got := MoveAntigravitySystemInstructionToReminder(payload)

	if role := ojson.GetBytes(got, "request.contents.0.role").String(); role != "user" {
		t.Fatalf("contents.0.role = %q, want user", role)
	}
	if text := ojson.GetBytes(got, "request.contents.0.parts.0.text").String(); text != "<EPHEMERAL_MESSAGE>\n"+AntigravitySystemReminderBasePrefix+"Rules\n</EPHEMERAL_MESSAGE>" {
		t.Fatalf("reminder text = %q", text)
	}
	if text := ojson.GetBytes(got, "request.contents.1.parts.0.text").String(); text != "hi" {
		t.Fatalf("model turn text = %q, want hi", text)
	}
}

func TestMoveAntigravitySystemInstructionToReminder_NoContents(t *testing.T) {
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Rules"}]}}}`)

	got := MoveAntigravitySystemInstructionToReminder(payload)

	if text := ojson.GetBytes(got, "request.contents.0.parts.0.text").String(); text != "<EPHEMERAL_MESSAGE>\n"+AntigravitySystemReminderBasePrefix+"Rules\n</EPHEMERAL_MESSAGE>" {
		t.Fatalf("reminder text = %q", text)
	}
}

func TestMoveAntigravitySystemInstructionToReminder_NoSystemInstruction(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

	got := MoveAntigravitySystemInstructionToReminder(payload)

	if text := ojson.GetBytes(got, "request.systemInstruction.parts.0.text").String(); text != antigravityIdentityPrefix {
		t.Fatalf("static system text = %q, want identity only", text)
	}
	if n := len(ojson.GetBytes(got, "request.contents.0.parts").Array()); n != 1 {
		t.Fatalf("contents parts = %d, want 1", n)
	}
	if text := ojson.GetBytes(got, "request.contents.0.parts.0.text").String(); text != wrappedHello {
		t.Fatalf("user text = %q, want %q", text, wrappedHello)
	}
}

func TestMoveAntigravitySystemInstructionToReminder_WrapsEveryUserTurn(t *testing.T) {
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Rules"}]},"contents":[{"role":"user","parts":[{"text":"one"}]},{"role":"model","parts":[{"text":"answer"}]},{"role":"user","parts":[{"text":"two"}]}]}}`)

	got := MoveAntigravitySystemInstructionToReminder(payload)

	if text := ojson.GetBytes(got, "request.contents.0.parts.1.text").String(); text != "<USER_REQUEST>\none\n</USER_REQUEST>" {
		t.Fatalf("first user text = %q", text)
	}
	if text := ojson.GetBytes(got, "request.contents.1.parts.0.text").String(); text != "answer" {
		t.Fatalf("model text = %q, want answer", text)
	}
	if text := ojson.GetBytes(got, "request.contents.2.parts.0.text").String(); text != "<USER_REQUEST>\ntwo\n</USER_REQUEST>" {
		t.Fatalf("second user text = %q", text)
	}
}

func TestMoveAntigravitySystemInstructionToReminder_EnvelopeSpansMultipleParts(t *testing.T) {
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Rules"}]},"contents":[{"role":"user","parts":[{"text":"first"},{"inlineData":{"mimeType":"image/png","data":"AA=="}},{"text":"last"}]}]}}`)

	got := MoveAntigravitySystemInstructionToReminder(payload)

	if text := ojson.GetBytes(got, "request.contents.0.parts.1.text").String(); text != "<USER_REQUEST>\nfirst" {
		t.Fatalf("first part = %q, want opening tag only", text)
	}
	if !ojson.GetBytes(got, "request.contents.0.parts.2.inlineData").Exists() {
		t.Fatalf("inlineData part must stay between the text parts: %s", got)
	}
	if text := ojson.GetBytes(got, "request.contents.0.parts.3.text").String(); text != "last\n</USER_REQUEST>" {
		t.Fatalf("last part = %q, want closing tag only", text)
	}
}

func TestMoveAntigravitySystemInstructionToReminder_LeavesNonUserTextUnwrapped(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"a","response":{}}}]},{"role":"user","parts":[{"text":"<system-reminder>\nctx\n</system-reminder>"},{"text":"<EPHEMERAL_MESSAGE>\nx\n</EPHEMERAL_MESSAGE>"},{"text":"  \n"},{"text":"<USER_REQUEST>\nnative\n</USER_REQUEST>"}]},{"role":"model","parts":[{"text":"answer"}]}]}}`)

	got := MoveAntigravitySystemInstructionToReminder(payload)

	if gotContents, wantContents := ojson.GetBytes(got, "request.contents").Raw, ojson.GetBytes(payload, "request.contents").Raw; gotContents != wantContents {
		t.Fatalf("contents changed:\n got %s\nwant %s", gotContents, wantContents)
	}
}

func TestMoveAntigravitySystemInstructionToReminder_UserRequestKeepsLiteralCharacters(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"a < b && \"c\""}]}]}}`)

	got := MoveAntigravitySystemInstructionToReminder(payload)

	if text := ojson.GetBytes(got, "request.contents.0.parts.0.text").String(); text != "<USER_REQUEST>\na < b && \"c\"\n</USER_REQUEST>" {
		t.Fatalf("user text = %q", text)
	}
	if strings.Contains(string(got), `\u003c`) || strings.Contains(string(got), `\u0026`) {
		t.Fatalf("payload must keep literal characters: %s", got)
	}
}

func TestUnwrapAntigravityUserRequest(t *testing.T) {
	cases := map[string]string{
		"<USER_REQUEST>\nhello\n</USER_REQUEST>": "hello",
		"<USER_REQUEST>\nhello":                  "hello",
		"hello":                                  "hello",
		"hello\n</USER_REQUEST>":                 "hello\n</USER_REQUEST>",
	}
	for in, want := range cases {
		if got := UnwrapAntigravityUserRequest(in); got != want {
			t.Fatalf("UnwrapAntigravityUserRequest(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMoveAntigravitySystemInstructionToReminder_BlankInstructionIsNotMoved(t *testing.T) {
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"  \n"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

	got := MoveAntigravitySystemInstructionToReminder(payload)

	if text := ojson.GetBytes(got, "request.systemInstruction.parts.0.text").String(); text != antigravityIdentityPrefix {
		t.Fatalf("static system text = %q, want identity only", text)
	}
	if n := len(ojson.GetBytes(got, "request.contents.0.parts").Array()); n != 1 {
		t.Fatalf("contents parts = %d, want 1", n)
	}
}
