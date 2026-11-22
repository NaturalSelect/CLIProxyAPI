package helps

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

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
	if text := ojson.GetBytes(got, "request.contents.0.parts.1.text").String(); text != "hello" {
		t.Fatalf("user text = %q, want hello", text)
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
	if text := ojson.GetBytes(got, "request.contents.1.parts.1.text").String(); text != "hello" {
		t.Fatalf("user text = %q, want hello", text)
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
	if text := ojson.GetBytes(got, "request.contents.0.parts.2.text").String(); text != "hello" {
		t.Fatalf("user text = %q, want hello", text)
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
	if text := ojson.GetBytes(got, "request.contents.0.parts.0.text").String(); text != "hello" {
		t.Fatalf("user text = %q, want hello", text)
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
